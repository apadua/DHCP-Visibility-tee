terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

# --- Mirror filter: capture only inbound DHCP client traffic to the server ----
# Traffic reaching the Infoblox ENI is already relayed (relay_ip:67 ->
# infoblox:67), so a single ingress rule on UDP/67 isolates client requests and
# excludes the server's replies (which are egress from the ENI). This keeps
# mirror volume — and therefore cost — near zero.
resource "aws_ec2_traffic_mirror_filter" "dhcp" {
  description = "DHCP client requests only"
}

resource "aws_ec2_traffic_mirror_filter_rule" "dhcp_ingress" {
  description              = "inbound UDP/67 (DHCP to server)"
  traffic_mirror_filter_id = aws_ec2_traffic_mirror_filter.dhcp.id
  traffic_direction        = "ingress"
  rule_number              = 100
  rule_action              = "accept"
  protocol                 = 17 # UDP
  source_cidr_block        = "0.0.0.0/0"
  destination_cidr_block   = "0.0.0.0/0"

  destination_port_range {
    from_port = 67
    to_port   = 67
  }
}

# --- Mirror target: the reformatter's ENI (or an NLB for HA) -----------------
resource "aws_ec2_traffic_mirror_target" "reformatter" {
  description          = "dhcp-tee reformatter"
  network_interface_id = var.reformatter_eni_id
}

# --- One session per DHCP-serving Infoblox ENI -------------------------------
# virtual_network_id is intentionally omitted so AWS auto-assigns a VNI per
# session; vxlan0 in external/collect_metadata mode accepts any VNI. If you run
# vxlan0 in pinned-VNI mode, set virtual_network_id here to that same value.
resource "aws_ec2_traffic_mirror_session" "infoblox" {
  for_each = toset(var.infoblox_eni_ids)

  description              = "mirror DHCP from ${each.value}"
  network_interface_id     = each.value
  traffic_mirror_target_id = aws_ec2_traffic_mirror_target.reformatter.id
  traffic_mirror_filter_id = aws_ec2_traffic_mirror_filter.dhcp.id
  session_number           = 1

  # packet_length is deliberately left unset = mirror the ENTIRE packet. Never
  # set this to a small value: it truncates DHCP options and starves the tool's
  # fingerprinting engine.
}

# --- Security group for the reformatter ENI ----------------------------------
resource "aws_security_group" "reformatter" {
  name        = "dhcp-tee-reformatter"
  description = "dhcp-tee: receive VXLAN mirror, send relayed DHCP to tools"
  vpc_id      = var.vpc_id

  ingress {
    description = "VXLAN traffic-mirror payload from Infoblox ENIs"
    from_port   = 4789
    to_port     = 4789
    protocol    = "udp"
    cidr_blocks = var.infoblox_cidr_blocks
  }

  egress {
    description = "relayed DHCP to visibility tools"
    from_port   = 67
    to_port     = 67
    protocol    = "udp"
    cidr_blocks = var.tool_cidr_blocks
  }

  # Uncomment if the host needs outbound 443 for SSM/patching:
  # egress {
  #   description = "https for SSM / updates"
  #   from_port   = 443
  #   to_port     = 443
  #   protocol    = "tcp"
  #   cidr_blocks = ["0.0.0.0/0"]
  # }

  tags = { Name = "dhcp-tee-reformatter" }
}
