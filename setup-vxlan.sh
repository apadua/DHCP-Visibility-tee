#!/usr/bin/env bash
# Create the kernel VXLAN interface that decapsulates AWS Traffic Mirroring
# traffic (VXLAN/UDP 4789) and presents the inner frames for capture on vxlan0.
#
# Default: external (collect_metadata) mode — accepts ANY VNI, so it handles
# multiple mirror sessions and AWS auto-assigned VNIs with no per-session config.
# If your kernel/distro misbehaves in external mode, switch to the pinned-VNI
# variant (MODE=pinned VNI=<n>); every mirror session must then be created with
# that same virtual_network_id.
set -euo pipefail

IFACE="${IFACE:-vxlan0}"
DSTPORT="${DSTPORT:-4789}"
MODE="${MODE:-external}"          # external | pinned
VNI="${VNI:-0}"                  # only used when MODE=pinned
UNDERLAY_DEV="${UNDERLAY_DEV:-}"  # optional, e.g. eth0 (pinned mode only)

ip link del "$IFACE" 2>/dev/null || true

if [[ "$MODE" == "pinned" ]]; then
  args=(type vxlan id "$VNI" dstport "$DSTPORT")
  [[ -n "$UNDERLAY_DEV" ]] && args+=(dev "$UNDERLAY_DEV")
  ip link add "$IFACE" "${args[@]}"
else
  ip link add "$IFACE" type vxlan external dstport "$DSTPORT"
fi

ip link set "$IFACE" up
# Belt-and-suspenders: the inner frame's dst MAC is the original client/relay
# MAC, not vxlan0's, so ensure we receive frames regardless of MAC.
ip link set "$IFACE" promisc on

echo "vxlan decap interface '$IFACE' is up (mode=$MODE dstport=$DSTPORT)"
