//go:build !linux

package main

import (
	"fmt"
	"runtime"

	"github.com/gopacket/gopacket"
)

// packetSource is the reading surface handle() consumes: a channel of parsed
// packets. *gopacket.PacketSource satisfies it, and so can a fake in tests.
type packetSource interface {
	Packets() chan gopacket.Packet
}

// openCapture is a stub on non-Linux platforms: AF_PACKET is Linux-only, so the
// service cannot capture here. This keeps the package building (and unit-testable)
// on macOS/Windows while making the runtime limitation explicit.
func openCapture(iface string) (packetSource, func(), error) {
	return nil, func() {}, fmt.Errorf("packet capture is only supported on Linux (AF_PACKET); this is %s", runtime.GOOS)
}
