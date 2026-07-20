// Command listen stands in for a visibility tool during integration testing: it
// waits for a single UDP datagram, verifies it parses as a relayed DHCP request
// with the expected giaddr, prints a summary, and exits 0. On timeout it exits 1
// so the calling script fails loudly.
//
// Usage:
//
//	go run ./testdata/listen -addr 127.0.0.1:6767 -expect-giaddr 10.1.2.3 -timeout 10s
package main

import (
	"flag"
	"log"
	"net"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:6767", "UDP address to listen on")
		giaddr  = flag.String("expect-giaddr", "", "if set, require this relay agent IP (giaddr)")
		timeout = flag.Duration("timeout", 10*time.Second, "how long to wait for a packet")
	)
	flag.Parse()

	ua, err := net.ResolveUDPAddr("udp", *addr)
	if err != nil {
		log.Fatalf("resolve %s: %v", *addr, err)
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(*timeout)); err != nil {
		log.Fatalf("set deadline: %v", err)
	}

	buf := make([]byte, 2048)
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		log.Fatalf("no packet within %s: %v", *timeout, err)
	}
	log.Printf("received %d bytes from %s", n, from)

	dhcp := &layers.DHCPv4{}
	if err := dhcp.DecodeFromBytes(buf[:n], gopacket.NilDecodeFeedback); err != nil {
		log.Fatalf("payload is not valid DHCP: %v", err)
	}
	if dhcp.Operation != layers.DHCPOpRequest {
		log.Fatalf("expected BOOTREQUEST, got op=%d", dhcp.Operation)
	}
	log.Printf("valid DHCP: xid=%#08x giaddr=%s chaddr=%s options=%d", dhcp.Xid, dhcp.RelayAgentIP, dhcp.ClientHWAddr, len(dhcp.Options))

	if *giaddr != "" && dhcp.RelayAgentIP.String() != *giaddr {
		log.Fatalf("giaddr mismatch: got %s want %s", dhcp.RelayAgentIP, *giaddr)
	}
	log.Printf("PASS: relayed DHCP request received and validated")
}
