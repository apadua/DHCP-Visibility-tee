# Contributing to dhcp-tee

Thanks for contributing. This project keeps testing reproducible so that a change
that passes for you passes for everyone — including the CI merge gate. Please run
the tests below before opening a PR.

## Prerequisites

- Go (see the version in [`go.mod`](go.mod); CI pins the same one).
- For the integration test: Linux with root, **or** Docker on any OS.

## Test layers

There are two layers, mirroring what CI runs.

### 1. Unit tests — run anywhere, no privileges

These cover the portable logic (`resolveDests`, `isDiscoverOrRequest`, and the
`handle` forward path). They run on macOS, Linux, and Windows.

```sh
make fmt-check   # gofmt is clean
make vet         # go vet passes
make test-race   # unit tests with the race detector
```

The AF_PACKET capture path is Linux-only and isolated in
[`capture_linux.go`](capture_linux.go); a stub in
[`capture_other.go`](capture_other.go) keeps the package building and testable on
other OSes.

### 2. Integration test — the real pipeline, no AWS

This exercises the actual capture + forward path end-to-end with a synthetic
mirror packet — no AWS and no live DHCP:

```
inject --(VXLAN/4789)--> kernel vxlan0 (decap) --> dhcp-tee (AF_PACKET)
dhcp-tee --(UDP)--> listen  (stands in for the visibility tool)
```

It creates `vxlan0`, runs `dhcp-tee`, injects a DHCP DISCOVER wrapped in VXLAN,
and asserts a valid relayed copy reaches a stand-in tool.

**On Linux (needs root for vxlan0 + AF_PACKET):**

```sh
sudo make test-integration
```

**On macOS/Windows (or to match CI exactly), run it in Docker:**

```sh
make test-integration-docker
```

## What CI does

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push and PR:

| Job | What it checks |
|---|---|
| `lint-and-test` | `gofmt`, `go vet`, and race-enabled unit tests on Ubuntu **and** macOS |
| `build` | cross-compiles the Linux binary for `amd64` and `arm64` |
| `integration` | runs the full pipeline (`testdata/integration-test.sh`) on Ubuntu |

Maintainers should enable branch protection on `main` requiring these checks, so
third-party PRs must pass the same tests before merge.

## Layout of the test tooling

```
main_test.go              unit tests (portable)
testdata/inject/          crafts a VXLAN-wrapped DHCP DISCOVER (mirror simulator)
testdata/listen/          minimal UDP tool stand-in that validates what it receives
testdata/integration-test.sh   wires it all together (Linux/root)
testdata/Dockerfile       reproducible env for the integration test
```
