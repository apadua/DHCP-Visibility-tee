//go:build linux

package main

import (
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

// packetSource is the reading surface handle() consumes: a channel of parsed
// packets. *gopacket.PacketSource satisfies it, and so can a fake in tests.
type packetSource interface {
	Packets() chan gopacket.Packet
}

// openCapture opens an AF_PACKET ring on iface and returns a packet source plus
// a close func. Linux-only: AF_PACKET requires a Linux kernel and CAP_NET_RAW.
func openCapture(iface string) (packetSource, func(), error) {
	tp, err := afpacket.NewTPacket(
		afpacket.OptInterface(iface),
		afpacket.OptFrameSize(frameSize),
		afpacket.OptBlockSize(blockSize),
		afpacket.OptNumBlocks(numBlocks),
	)
	if err != nil {
		return nil, func() {}, err
	}
	src := gopacket.NewPacketSource(tp, layers.LayerTypeEthernet)
	src.NoCopy = true
	return src, tp.Close, nil
}
