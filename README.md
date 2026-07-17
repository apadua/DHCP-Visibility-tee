# dhcp-tee

A passive **DHCP visibility tee** for AWS. It captures relayed DHCP client
messages arriving at your Infoblox (or any) DHCP server ENIs via VPC Traffic
Mirroring, and re-emits them **unchanged** to one or more tools that expect
relay-formatted DHCP — exactly what an `ip helper-address` target receives from
a switch, reconstructed in software because there's no switch to configure.

It is a **copy**, never an inline relay. The real DHCP server answers the real
client; this service only forwards a duplicate of the request. A dropped copy
costs you one fingerprint until the next renewal and nothing else — it can never
perturb production DHCP.

```
Infoblox ENI  ──(VPC mirror, VXLAN/4789)──▶  vxlan0 (kernel decap)
vxlan0        ──(AF_PACKET ring)──────────▶  dhcp-tee
dhcp-tee      ──(UDP :67 ─▶ tool:67)──────▶  visibility tool(s)
```

## Why this design

Because your clients reach the server through an **upstream relay you don't
control**, the packets arriving at the Infoblox ENI are **already
relay-formatted**: `giaddr` is populated, unicast from the relay to UDP/67. That
makes the "reformatter" almost trivial — it forwards the DHCP payload
**byte-for-byte**, so `giaddr` and every option (55/60/61/12/77…) reach your
tool intact for fingerprinting. The only thing that changes is the L3/L4
envelope (source = this host, dest = your tool), which the kernel builds when
`dhcp-tee` sends from its own UDP socket.

Mirroring happens at the **ENI / Nitro layer**, so **nothing runs on the
Infoblox appliance** — no code, no shell access, no changes to the appliance or
the upstream relay. Current vNIOS for AWS runs on M7i/R7i/R6i (all Nitro), which
is the only requirement for VPC Traffic Mirroring.

## Repo layout

```
cmd/dhcp-tee/main.go     the reformatter (pure Go, static binary, no libpcap)
deploy/setup-vxlan.sh    creates the vxlan0 decap interface
deploy/vxlan0.service    systemd oneshot for vxlan0
deploy/dhcp-tee.service  systemd unit for the service (least-privilege)
terraform/               mirror filter + rule + target + sessions + SG
Makefile                 static cross-build
```

## Build

Pure Go, no cgo, no libpcap — one static binary:

```sh
make build GOARCH=arm64      # t4g; use amd64 for x86 instances
# -> bin/dhcp-tee
```

## Deploy

### 1. AWS side (Terraform)

```sh
cd terraform
cp terraform.tfvars.example terraform.tfvars   # then edit
terraform init && terraform apply
```

Attach the emitted `reformatter_security_group_id` to the reformatter ENI. Point
`infoblox_eni_ids` at **every DHCP-serving member** (an Infoblox HA pair / Grid
has more than one), and be sure to mirror the **service interface**, not the
management (LAN1) interface.

### 2. Reformatter host

Any small Linux instance in the VPC (`t4g.small` is generous — DHCP is bursty,
not a stream). Then:

```sh
sudo useradd -r -s /usr/sbin/nologin dhcp-tee
sudo install -m0755 bin/dhcp-tee            /usr/local/bin/dhcp-tee
sudo install -m0755 deploy/setup-vxlan.sh   /usr/local/sbin/setup-vxlan.sh
sudo install -m0644 deploy/vxlan0.service   /etc/systemd/system/vxlan0.service
sudo install -m0644 deploy/dhcp-tee.service /etc/systemd/system/dhcp-tee.service

# set DHCP_TEE_TOOLS in dhcp-tee.service to your tool IP(s), then:
sudo systemctl daemon-reload
sudo systemctl enable --now vxlan0.service dhcp-tee.service
```

The service runs unprivileged with only `CAP_NET_RAW` (capture) and
`CAP_NET_BIND_SERVICE` (source from UDP/67).

## Verify

```sh
# 1. Encapsulated mirror traffic is arriving on the primary NIC:
sudo tcpdump -ni eth0 udp port 4789

# 2. Kernel is decapsulating onto vxlan0 (you should see inner DHCP):
sudo tcpdump -ni vxlan0 udp port 67

# 3. Copies are leaving toward the tool:
sudo tcpdump -ni eth0 'udp port 67 and dst host <TOOL_IP>'

# 4. Counters:
journalctl -u dhcp-tee -f    # "stats: received=… forwarded=… no_giaddr=…"
```

If `received` climbs but `forwarded` is 0, check the tool route/SG/NACL. If
`no_giaddr` is climbing, raw client broadcasts are leaking in (see below).

## Configuration

`dhcp-tee` reads flags or env vars:

| flag | env | default | meaning |
|---|---|---|---|
| `-iface` | `DHCP_TEE_IFACE` | `vxlan0` | decapsulated capture interface |
| `-tools` | `DHCP_TEE_TOOLS` | — | comma-separated tool IPs (required) |
| `-src-ip` | `DHCP_TEE_SRC_IP` | kernel-chosen | override source IP for copies |
| `-discover-request-only` | — | `false` | forward only Option 53 = 1/3 |
| `-log-each` | — | `false` | log every forwarded packet |

By default it forwards **all** client messages (BOOTREQUEST), which is what a
real relay does. Use `-discover-request-only` to narrow to DISCOVER/REQUEST.

## Notes & gotchas

- **Trusted-relay allowlist.** The tool sees a source IP of *this host* (not the
  original relay). Add the reformatter's IP to the tool's trusted-relay/source
  allowlist or it will silently drop the feed. Using one source IP is
  deliberate — one allowlist entry instead of one per upstream relay — and it
  keeps the AWS **source/dest check** happy (packets leave with this ENI's real
  address, so you don't disable the check or "spoof" the relay IP).

- **`packet_length`.** The Terraform leaves it unset on purpose = mirror the
  whole packet. Truncation chops DHCP options and starves fingerprinting.

- **VNI mode.** `vxlan0` defaults to external/collect_metadata mode, which
  accepts *any* VNI — so multiple sessions and AWS auto-assigned VNIs "just
  work." If your kernel misbehaves, switch to pinned mode (`MODE=pinned VNI=n`
  in `vxlan0.service`) and set `virtual_network_id = n` on every session.

- **HA.** Because this is a non-inline tee, redundancy is optional polish. If you
  want it, make the mirror target an NLB in front of a 2-instance ASG; otherwise
  a single instance with an ASG min/max of 1 (auto-replace) is enough. Losing a
  node delays a fingerprint, nothing more.

- **giaddr injection (only if needed).** The traffic is guaranteed relayed, so
  `giaddr` is already set and is preserved as-is. If raw client broadcasts ever
  leak in (`no_giaddr` counter climbing), the tool can't scope-bind them. To
  inject a `giaddr` from a subnet→gateway map, do it in `handle()` on
  `dhcp.RelayAgentIP`, then re-serialize the DHCP layer with
  `gopacket.SerializeLayers` before forwarding instead of sending `udp.Payload`
  verbatim. Left out by default to keep the hot path a pure copy.

- **Higher throughput.** AF_PACKET + userspace filter is plenty for DHCP volume.
  If you ever repurpose this for a firehose, attach a kernel BPF program to the
  ring rather than filtering in Go.
