// Command dhcp-tee passively copies relayed DHCP client messages captured via
// AWS VPC Traffic Mirroring and re-emits them, unchanged, to one or more
// visibility tools — behaving like an `ip helper-address` relay target without
// ever touching the production DHCP path.
//
// Capture path:
//
//	Infoblox ENI  --(VPC mirror, VXLAN/4789)-->  vxlan0 (kernel decap)
//	vxlan0        --(AF_PACKET ring)-->          this service
//	this service  --(UDP :67 -> tool:67)-->      visibility tool
//
// Because the mirrored traffic is already relay-formatted (giaddr populated by
// the upstream relay), the DHCP payload is forwarded byte-for-byte: giaddr and
// every option (55/60/61/12/77...) are preserved for the tool's fingerprinting
// engine. Only the L3/L4 envelope changes, and the kernel builds that when we
// send from our own UDP socket.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/afpacket"
	"github.com/gopacket/gopacket/layers"
)

const (
	// AF_PACKET ring geometry. frameSize must be a power of two; blockSize a
	// multiple of both the page size (4096) and frameSize. DHCP frames are tiny
	// (<600B), so 4096 never truncates; the ring is deliberately modest.
	frameSize = 4096
	blockSize = frameSize * 32 // 131072
	numBlocks = 16
)

type stats struct {
	received  atomic.Uint64
	forwarded atomic.Uint64
	notReq    atomic.Uint64
	noGiaddr  atomic.Uint64
	sendErr   atomic.Uint64
}

func main() {
	var (
		iface   = flag.String("iface", envOr("DHCP_TEE_IFACE", "vxlan0"), "decapsulated capture interface")
		toolCSV = flag.String("tools", os.Getenv("DHCP_TEE_TOOLS"), "comma-separated tool IPs to forward to")
		srcIP   = flag.String("src-ip", os.Getenv("DHCP_TEE_SRC_IP"), "override source IP for forwarded copies (default: kernel picks the ENI IP)")
		srcPort = flag.Int("src-port", 67, "UDP source port for forwarded copies")
		dstPort = flag.Int("dst-port", 67, "UDP destination port on the tool")
		reqOnly = flag.Bool("discover-request-only", false, "forward only DHCPDISCOVER/REQUEST (Option 53 = 1/3) instead of all client messages")
		logEach = flag.Bool("log-each", false, "log every forwarded packet (verbose)")
		statInt = flag.Duration("stats-interval", 60*time.Second, "how often to log counters")
	)
	flag.Parse()

	if strings.TrimSpace(*toolCSV) == "" {
		log.Fatal("no tools configured: set -tools or DHCP_TEE_TOOLS (comma-separated IPs)")
	}
	dests, err := resolveDests(*toolCSV, *dstPort)
	if err != nil {
		log.Fatalf("parsing -tools: %v", err)
	}

	// Send socket bound to the source port. Source IP is normally left to the
	// kernel so it uses this host's ENI address, which passes the AWS
	// source/dest check and needs exactly one entry (this host) in the tool's
	// trusted-relay list.
	laddr := &net.UDPAddr{Port: *srcPort}
	if *srcIP != "" {
		ip := net.ParseIP(*srcIP)
		if ip == nil {
			log.Fatalf("invalid -src-ip %q", *srcIP)
		}
		laddr.IP = ip
	}
	send, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		log.Fatalf("bind udp :%d (needs CAP_NET_BIND_SERVICE): %v", *srcPort, err)
	}
	defer send.Close()

	tp, err := afpacket.NewTPacket(
		afpacket.OptInterface(*iface),
		afpacket.OptFrameSize(frameSize),
		afpacket.OptBlockSize(blockSize),
		afpacket.OptNumBlocks(numBlocks),
	)
	if err != nil {
		log.Fatalf("opening AF_PACKET on %s (needs CAP_NET_RAW, iface up): %v", *iface, err)
	}
	defer tp.Close()

	var st stats
	go reportStats(&st, *statInt)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("shutting down; received=%d forwarded=%d", st.received.Load(), st.forwarded.Load())
		tp.Close()
		send.Close()
		os.Exit(0)
	}()

	log.Printf("dhcp-tee up: iface=%s src=:%d tools=%s reqOnly=%v", *iface, *srcPort, *toolCSV, *reqOnly)

	src := gopacket.NewPacketSource(tp, layers.LayerTypeEthernet)
	src.NoCopy = true
	for pkt := range src.Packets() {
		handle(pkt, send, dests, &st, *reqOnly, *logEach)
	}
}

func handle(pkt gopacket.Packet, send *net.UDPConn, dests []*net.UDPAddr, st *stats, reqOnly, logEach bool) {
	udpLayer := pkt.Layer(layers.LayerTypeUDP)
	if udpLayer == nil {
		return
	}
	udp := udpLayer.(*layers.UDP)
	if udp.DstPort != 67 {
		return
	}
	dhcpLayer := pkt.Layer(layers.LayerTypeDHCPv4)
	if dhcpLayer == nil {
		return
	}
	dhcp := dhcpLayer.(*layers.DHCPv4)

	st.received.Add(1)

	// Only client-originated messages (BOOTREQUEST). A real relay relays all of
	// them; that's the default. -discover-request-only narrows to Option 53=1/3.
	if dhcp.Operation != layers.DHCPOpRequest {
		st.notReq.Add(1)
		return
	}
	if reqOnly && !isDiscoverOrRequest(dhcp) {
		st.notReq.Add(1)
		return
	}

	// The traffic is guaranteed relayed, so giaddr should be set. If it isn't, a
	// raw client broadcast leaked in and the tool can't scope-bind it. We still
	// forward (a relay would too) but count it so a spike is visible. To instead
	// inject giaddr from a subnet map, do it here on dhcp.RelayAgentIP and
	// re-serialize the DHCP layer before forwarding; see README.
	if dhcp.RelayAgentIP == nil || dhcp.RelayAgentIP.IsUnspecified() {
		st.noGiaddr.Add(1)
	}

	// Forward the DHCP message verbatim: giaddr + all options preserved.
	payload := udp.Payload
	for _, d := range dests {
		if _, err := send.WriteToUDP(payload, d); err != nil {
			st.sendErr.Add(1)
			log.Printf("send to %s failed: %v", d.IP, err)
			continue
		}
		st.forwarded.Add(1)
	}
	if logEach {
		log.Printf("relayed xid=%#08x giaddr=%s chaddr=%s -> %d tool(s)", dhcp.Xid, dhcp.RelayAgentIP, dhcp.ClientHWAddr, len(dests))
	}
}

func isDiscoverOrRequest(d *layers.DHCPv4) bool {
	for _, o := range d.Options {
		if o.Type == layers.DHCPOptMessageType && len(o.Data) == 1 {
			mt := layers.DHCPMsgType(o.Data[0])
			return mt == layers.DHCPMsgTypeDiscover || mt == layers.DHCPMsgTypeRequest
		}
	}
	return false
}

func resolveDests(csv string, port int) ([]*net.UDPAddr, error) {
	var out []*net.UDPAddr
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP %q", s)
		}
		out = append(out, &net.UDPAddr{IP: ip, Port: port})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid tool IPs in %q", csv)
	}
	return out, nil
}

func reportStats(st *stats, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		log.Printf("stats: received=%d forwarded=%d not_request=%d no_giaddr=%d send_err=%d",
			st.received.Load(), st.forwarded.Load(), st.notReq.Load(), st.noGiaddr.Load(), st.sendErr.Load())
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
