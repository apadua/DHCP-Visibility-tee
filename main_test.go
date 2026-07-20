package main

import (
	"net"
	"testing"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

// fakeUDP records everything handle() would have sent, so tests can assert the
// forward behaviour without opening a real socket.
type fakeUDP struct {
	writes []struct {
		payload []byte
		addr    *net.UDPAddr
	}
	err error // if set, WriteToUDP fails with it
}

func (f *fakeUDP) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	f.writes = append(f.writes, struct {
		payload []byte
		addr    *net.UDPAddr
	}{cp, addr})
	return len(b), nil
}

// buildDHCPPacket serialises Ethernet/IP/UDP/DHCPv4 the same way a decapsulated
// mirror frame looks on vxlan0, then re-parses it so handle() sees a realistic
// gopacket.Packet.
func buildDHCPPacket(t *testing.T, op layers.DHCPOp, msgType layers.DHCPMsgType, giaddr net.IP, dstPort layers.UDPPort) gopacket.Packet {
	t.Helper()

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
		SrcIP:    net.IPv4(10, 0, 0, 1),
		DstIP:    net.IPv4(10, 0, 0, 2),
	}
	udp := &layers.UDP{
		SrcPort: 68,
		DstPort: dstPort,
	}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatalf("checksum setup: %v", err)
	}

	dhcp := &layers.DHCPv4{
		Operation:    op,
		HardwareType: layers.LinkTypeEthernet,
		HardwareLen:  6,
		Xid:          0xdeadbeef,
		ClientHWAddr: net.HardwareAddr{0x02, 0x11, 0x22, 0x33, 0x44, 0x55},
	}
	if giaddr != nil {
		dhcp.RelayAgentIP = giaddr
	}
	dhcp.Options = append(dhcp.Options, layers.NewDHCPOption(layers.DHCPOptMessageType, []byte{byte(msgType)}))

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, eth, ip, udp, dhcp); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return gopacket.NewPacket(buf.Bytes(), layers.LayerTypeEthernet, gopacket.Default)
}

func TestResolveDests(t *testing.T) {
	tests := []struct {
		name    string
		csv     string
		port    int
		wantN   int
		wantErr bool
	}{
		{"single", "10.0.0.5", 67, 1, false},
		{"multiple", "10.0.0.5,10.0.0.6", 67, 2, false},
		{"whitespace and empties", " 10.0.0.5 , ,10.0.0.6 ", 67, 2, false},
		{"empty", "", 67, 0, true},
		{"only commas", ",,", 67, 0, true},
		{"invalid ip", "10.0.0.5,notanip", 67, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDests(tc.csv, tc.port)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.wantN {
				t.Fatalf("got %d dests, want %d", len(got), tc.wantN)
			}
			for _, d := range got {
				if d.Port != tc.port {
					t.Errorf("port = %d, want %d", d.Port, tc.port)
				}
			}
		})
	}
}

func TestIsDiscoverOrRequest(t *testing.T) {
	tests := []struct {
		name string
		mt   layers.DHCPMsgType
		want bool
	}{
		{"discover", layers.DHCPMsgTypeDiscover, true},
		{"request", layers.DHCPMsgTypeRequest, true},
		{"offer", layers.DHCPMsgTypeOffer, false},
		{"ack", layers.DHCPMsgTypeAck, false},
		{"release", layers.DHCPMsgTypeRelease, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &layers.DHCPv4{}
			d.Options = append(d.Options, layers.NewDHCPOption(layers.DHCPOptMessageType, []byte{byte(tc.mt)}))
			if got := isDiscoverOrRequest(d); got != tc.want {
				t.Errorf("isDiscoverOrRequest = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("no message type option", func(t *testing.T) {
		if isDiscoverOrRequest(&layers.DHCPv4{}) {
			t.Error("expected false when Option 53 is absent")
		}
	})
}

func TestHandle(t *testing.T) {
	giaddr := net.IPv4(10, 1, 2, 3).To4()
	dests := []*net.UDPAddr{
		{IP: net.IPv4(192, 0, 2, 10), Port: 67},
		{IP: net.IPv4(192, 0, 2, 11), Port: 67},
	}

	t.Run("forwards relayed request to all tools", func(t *testing.T) {
		pkt := buildDHCPPacket(t, layers.DHCPOpRequest, layers.DHCPMsgTypeDiscover, giaddr, 67)
		fake := &fakeUDP{}
		var st stats
		handle(pkt, fake, dests, &st, false, false)

		if len(fake.writes) != 2 {
			t.Fatalf("wrote to %d tools, want 2", len(fake.writes))
		}
		if st.received.Load() != 1 || st.forwarded.Load() != 2 {
			t.Fatalf("received=%d forwarded=%d, want 1/2", st.received.Load(), st.forwarded.Load())
		}
		if st.noGiaddr.Load() != 0 {
			t.Errorf("noGiaddr=%d, want 0 (giaddr was set)", st.noGiaddr.Load())
		}
	})

	t.Run("drops server replies (BOOTREPLY)", func(t *testing.T) {
		pkt := buildDHCPPacket(t, layers.DHCPOpReply, layers.DHCPMsgTypeOffer, giaddr, 67)
		fake := &fakeUDP{}
		var st stats
		handle(pkt, fake, dests, &st, false, false)

		if len(fake.writes) != 0 {
			t.Fatalf("forwarded a reply; want 0 writes")
		}
		if st.notReq.Load() != 1 {
			t.Errorf("notReq=%d, want 1", st.notReq.Load())
		}
	})

	t.Run("ignores non-DHCP destination port", func(t *testing.T) {
		pkt := buildDHCPPacket(t, layers.DHCPOpRequest, layers.DHCPMsgTypeDiscover, giaddr, 9999)
		fake := &fakeUDP{}
		var st stats
		handle(pkt, fake, dests, &st, false, false)

		if len(fake.writes) != 0 || st.received.Load() != 0 {
			t.Fatalf("should have ignored non-67 packet: writes=%d received=%d", len(fake.writes), st.received.Load())
		}
	})

	t.Run("reqOnly drops non-discover/request", func(t *testing.T) {
		pkt := buildDHCPPacket(t, layers.DHCPOpRequest, layers.DHCPMsgTypeRelease, giaddr, 67)
		fake := &fakeUDP{}
		var st stats
		handle(pkt, fake, dests, &st, true, false)

		if len(fake.writes) != 0 {
			t.Fatalf("reqOnly should drop RELEASE; got %d writes", len(fake.writes))
		}
		if st.notReq.Load() != 1 {
			t.Errorf("notReq=%d, want 1", st.notReq.Load())
		}
	})

	t.Run("counts missing giaddr but still forwards", func(t *testing.T) {
		pkt := buildDHCPPacket(t, layers.DHCPOpRequest, layers.DHCPMsgTypeDiscover, nil, 67)
		fake := &fakeUDP{}
		var st stats
		handle(pkt, fake, dests, &st, false, false)

		if st.noGiaddr.Load() != 1 {
			t.Errorf("noGiaddr=%d, want 1", st.noGiaddr.Load())
		}
		if st.forwarded.Load() != 2 {
			t.Errorf("forwarded=%d, want 2 (still forwards)", st.forwarded.Load())
		}
	})

	t.Run("counts send errors", func(t *testing.T) {
		pkt := buildDHCPPacket(t, layers.DHCPOpRequest, layers.DHCPMsgTypeDiscover, giaddr, 67)
		fake := &fakeUDP{err: net.ErrClosed}
		var st stats
		handle(pkt, fake, dests, &st, false, false)

		if st.sendErr.Load() != 2 {
			t.Errorf("sendErr=%d, want 2", st.sendErr.Load())
		}
		if st.forwarded.Load() != 0 {
			t.Errorf("forwarded=%d, want 0", st.forwarded.Load())
		}
	})

	t.Run("preserves payload byte-for-byte", func(t *testing.T) {
		pkt := buildDHCPPacket(t, layers.DHCPOpRequest, layers.DHCPMsgTypeDiscover, giaddr, 67)
		udp := pkt.Layer(layers.LayerTypeUDP).(*layers.UDP)
		want := udp.Payload

		fake := &fakeUDP{}
		var st stats
		handle(pkt, fake, []*net.UDPAddr{dests[0]}, &st, false, false)

		if len(fake.writes) != 1 {
			t.Fatalf("want 1 write, got %d", len(fake.writes))
		}
		got := fake.writes[0].payload
		if len(got) != len(want) {
			t.Fatalf("payload length changed: got %d want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("payload differs at byte %d: got %#x want %#x", i, got[i], want[i])
			}
		}
	})
}
