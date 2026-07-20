#!/usr/bin/env bash
# End-to-end integration test for dhcp-tee, with NO AWS required.
#
# It exercises the real capture+forward pipeline:
#
#   inject --(VXLAN/4789)--> kernel vxlan0 (decap) --> dhcp-tee (AF_PACKET)
#   dhcp-tee --(UDP)--> listen (stands in for the visibility tool)
#
# Requires Linux + root (or CAP_NET_ADMIN for vxlan0 and CAP_NET_RAW for capture).
# Run it inside the provided Docker image (see testdata/Dockerfile) so it is
# reproducible for every contributor and in CI.
#
# Exit 0 = the injected DHCP DISCOVER made it through and was validated.
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "integration test is Linux-only (needs kernel VXLAN + AF_PACKET); skipping on $(uname -s)" >&2
  exit 0
fi
if [[ "${EUID}" -ne 0 ]]; then
  echo "integration test needs root (vxlan0 + AF_PACKET). Re-run as root or in Docker." >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

IFACE="${IFACE:-vxlan0}"
TOOL_ADDR="127.0.0.1:6767"
TOOL_IP="127.0.0.1"
TOOL_PORT="6767"
GIADDR="10.1.2.3"
VNI="${VNI:-42}"

export PATH="$PATH:$(go env GOPATH)/bin"

cleanup() {
  set +e
  [[ -n "${TEE_PID:-}" ]] && kill "$TEE_PID" 2>/dev/null
  [[ -n "${LISTEN_PID:-}" ]] && kill "$LISTEN_PID" 2>/dev/null
  ip link del "$IFACE" 2>/dev/null
}
trap cleanup EXIT

echo "==> building binaries"
go build -o bin/dhcp-tee .
go build -o bin/inject ./testdata/inject
go build -o bin/listen ./testdata/listen

echo "==> creating $IFACE (external/collect_metadata mode)"
IFACE="$IFACE" bash ./setup-vxlan.sh
ip addr add 127.0.0.2/32 dev "$IFACE" 2>/dev/null || true

echo "==> starting listener (stand-in tool) on $TOOL_ADDR"
bin/listen -addr "$TOOL_ADDR" -expect-giaddr "$GIADDR" -timeout 15s &
LISTEN_PID=$!
sleep 0.5

echo "==> starting dhcp-tee capturing $IFACE, forwarding to $TOOL_IP:$TOOL_PORT"
bin/dhcp-tee -iface "$IFACE" -tools "$TOOL_IP" -dst-port "$TOOL_PORT" -src-port 6800 -log-each -stats-interval 5s &
TEE_PID=$!
sleep 1

echo "==> injecting synthetic VXLAN-wrapped DHCP DISCOVER"
bin/inject -underlay 127.0.0.1 -vni "$VNI" -giaddr "$GIADDR"

echo "==> waiting for listener to validate"
if wait "$LISTEN_PID"; then
  echo "INTEGRATION TEST PASSED"
  exit 0
else
  echo "INTEGRATION TEST FAILED: tool did not receive a valid relayed DHCP packet" >&2
  exit 1
fi
