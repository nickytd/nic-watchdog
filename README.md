# nic-watchdog

![CI](https://github.com/nickytd/nic-watchdog/actions/workflows/ci.yml/badge.svg)
![Release](https://github.com/nickytd/nic-watchdog/actions/workflows/release.yml/badge.svg)
![Go Version](https://img.shields.io/github/go-mod/go-version/nickytd/nic-watchdog)
![License](https://img.shields.io/github/license/nickytd/nic-watchdog)
![Latest Release](https://img.shields.io/github/v/release/nickytd/nic-watchdog)

A lightweight Go daemon that monitors Ethernet connectivity on Raspberry Pi nodes and automatically recovers from spontaneous link drops caused by known hardware bugs in the Ethernet PHY.

## Problem

Raspberry Pi 5 and some Raspberry Pi 4 boards have a known hardware-level bug where the NIC spontaneously drops link or enters a unidirectional TX failure state — the Pi can receive packets but transmitted packets never arrive. The drops occur at random intervals with zero preceding kernel errors.

When the link drops, a simple `ip link down/up` cycle usually restores connectivity. However, in some cases the PHY TX path is only partially restored — enough for local subnet traffic but not for routed traffic to external IPs.

### Upstream issues

- [raspberrypi/linux#6420](https://github.com/raspberrypi/linux/issues/6420) — RPi5 macb/BCM54213PE, open since Oct 2024
- [raspberrypi/firmware#1922](https://github.com/raspberrypi/firmware/issues/1922) — RPi5 firmware-level, confirms bug occurs before Linux starts
- [raspberrypi/linux#3108](https://github.com/raspberrypi/linux/issues/3108) — RPi4 genet/BCM54210PE, open since Jul 2019

No upstream fix is available.

## How it works

`nic-watchdog` runs as a long-lived systemd service and checks external connectivity via ICMP ping every 10 seconds. When a failure is detected, it distinguishes between a full link-down (both gateway and external unreachable) and a partial TX failure (gateway reachable but external is not), then escalates recovery:

| Step | Condition | Action |
|------|-----------|--------|
| 1 | Gateway up, external down | Flush ARP cache and route cache |
| 2 | Still down after step 1 | Restart `systemd-networkd` |
| 3 | Still down after step 2 | Full interface down/up cycle |
| — | Both gateway and external down | Full interface down/up cycle immediately |

A cooldown (default 10 minutes) prevents repeated full cycles. All state is in-memory — no files in `/run`.

## Usage

```
nic-watchdog [flags]

Flags:
      --check-interval duration   Check interval (default 10s)
      --cooldown duration         Minimum time between full interface cycles (default 10m0s)
      --gateway string            Gateway IP (auto-detected if empty)
      --iface string              Network interface (default "eth0")
      --ping-target string        External connectivity check target (default "8.8.8.8")
      --soft-max int              Max soft recovery attempts before escalating (default 3)
```

All flags can also be set via environment variables with the `NIC_WATCHDOG_` prefix:

```bash
NIC_WATCHDOG_IFACE=eth1 NIC_WATCHDOG_COOLDOWN=5m nic-watchdog
```

## Build

Requires Go 1.26+. Cross-compile for arm64:

```bash
make build
```

The binary is written to `bin/nic-watchdog`.

Available make targets:

```
make fmt          # Format code (gofmt + goimports)
make lint         # Run golangci-lint
make license      # Apply SPDX license headers
make check        # fmt + lint + license + go fix
make tidy         # go mod tidy
make build        # check + cross-compile
make clean        # Remove bin/
make deploy       # Build + Ansible deploy to all hosts
```

## Deploy

The `deploy` target builds the binary and runs the Ansible playbook to deploy to all target nodes:

```bash
make deploy
```

The playbook (`deploy/playbook.yml`) targets `pis`, `deskpis`, and `turingpis` host groups.

## Systemd service

Runs as `Type=simple` with `Restart=always`. Requires `CAP_NET_ADMIN` (interface cycling) and `CAP_NET_RAW` (ICMP ping).

```ini
[Service]
Type=simple
ExecStart=/usr/local/bin/nic-watchdog --iface eth0 --ping-target 8.8.8.8 --cooldown 600s --soft-max 3
Restart=always
RestartSec=5
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
```

## Journal output

```
level=INFO msg="discovered gateway" gateway=192.168.1.1
level=INFO msg="watchdog started" iface=eth0 target=8.8.8.8 gateway=192.168.1.1 interval=10s
level=INFO msg="external unreachable, gateway up — flushing ARP and routes" gateway=192.168.1.1 attempt=1
level=INFO msg="external connectivity restored" after_soft_attempts=1
level=INFO msg="cycling interface" iface=eth0 gateway=192.168.1.1 target=8.8.8.8
level=INFO msg="connectivity restored after cycle"
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
