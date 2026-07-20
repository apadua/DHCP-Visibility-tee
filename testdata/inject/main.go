// Command inject crafts a synthetic AWS-mirror packet — a DHCP DISCOVER wrapped
// in Ethernet/IP/UDP and then in VXLAN — and sends it to the local VXLAN
// underlay port (UDP/4789). The kernel's vxlan0 interface decapsulates it, which
// is exactly what dhcp-tee captures. This lets the whole pipeline be exercised
// end-to-end with no AWS and no real DHCP traffic.
//
// Usage:
//
//	go run ./testdata/inject -underlay 127.0.0.1 -vni 42 -giaddr 10.1.2.3
package main

import (
	"flag"
	"log"
	"net"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

func main() {
	var (
		underlay = flag.String("underlay", "127.0.0.1", "underlay IP hosting vxlan0 (VXLAN/4789 listener)")
		port     = flag.Int("port", 4789, "VXLAN underlay UDP port")
		vni      = flag.Int("vni", 0, "VXLAN VNI (0 works with external/collect_metadata vxlan0)")
		giaddr   = flag.String("giaddr", "10.1.2.3", "relay agent IP (giaddr) to embed; empty to omit")
	)
	flag.Parse()

	inner := buildInnerDHCP(*giaddr)

	vx := &layers.VXLAN{ValidIDFlag: true}
	vx.VNI = uint32(*vni)

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, vx, gopacket.Payload(inner)); err != nil {
		log.Fatalf("serialize vxlan: %v", err)
	}

	dst := &net.UDPAddr{IP: net.ParseIP(*underlay), Port: *port}
	conn, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		log.Fatalf("dial %s: %v", dst, err)
	}
	defer conn.Close()
	sent := buf.Bytes()
	if _, err := conn.Write(sent); err != nil {
		log.Fatalf("send: %v", err)
	}
	log.Printf("injected VXLAN(vni=%d) DHCP DISCOVER giaddr=%s -> %s (%d bytes)", *vni, *giaddr, dst, len(sent))
}

// buildInnerDHCP serialises the decapsulated inner frame: Ethernet/IP/UDP/DHCPv4,
// a relay-formatted DISCOVER with giaddr set, unicast to UDP/67 like a real relay.
func buildInnerDHCP(giaddr string) []byte {
	eth := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
		DstMAC:       net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		Protocol: layers.IPProtocolUDP,
		SrcIP:    net.IPv4(10, 0, 0, 1), // upstream relay
		DstIP:    net.IPv4(10, 0, 0, 2), // Infoblox ENI
	}
	udp := &layers.UDP{SrcPort: 67, DstPort: 67}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		log.Fatalf("checksum setup: %v", err)
	}

	dhcp := &layers.DHCPv4{
		Operation:    layers.DHCPOpRequest,
		HardwareType: layers.LinkTypeEthernet,
		HardwareLen:  6,
		Xid:          0xdeadbeef,
		ClientHWAddr: net.HardwareAddr{0x02, 0x11, 0x22, 0x33, 0x44, 0x55},
	}
	if giaddr != "" {
		if ip := net.ParseIP(giaddr); ip != nil {
			dhcp.RelayAgentIP = ip.To4()
		}
	}
	dhcp.Options = append(dhcp.Options,
		layers.NewDHCPOption(layers.DHCPOptMessageType, []byte{byte(layers.DHCPMsgTypeDiscover)}),
		layers.NewDHCPOption(layers.DHCPOptParamsRequest, []byte{1, 3, 6, 15}),
		layers.NewDHCPOption(layers.DHCPOptClassID, []byte("test-vendor")),
	)

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, eth, ip, udp, dhcp); err != nil {
		log.Fatalf("serialize inner: %v", err)
	}
	return buf.Bytes()
}
