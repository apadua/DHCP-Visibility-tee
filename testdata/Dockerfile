# Reproducible environment for the Linux integration test. It bundles the Go
# toolchain and iproute2 (for `ip link add ... type vxlan`). Run it privileged so
# the container can create vxlan0 and open an AF_PACKET ring:
#
#   docker build -f testdata/Dockerfile -t dhcp-tee-itest .
#   docker run --rm --privileged dhcp-tee-itest
#
# The image runs testdata/integration-test.sh as its entrypoint.
FROM golang:1.22-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends iproute2 tcpdump ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ENTRYPOINT ["bash", "testdata/integration-test.sh"]
