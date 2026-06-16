// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// pingSeq supplies a unique 16-bit sequence number per ping call, so concurrent
// or back-to-back pings on the shared ICMP socket can't accept each other's
// replies. The kernel rewrites the Echo ID to the socket's ephemeral source
// port on "udp4" mode, so Seq is the only field we control end-to-end.
var pingSeq atomic.Uint32

func ping(ctx context.Context, addr string, timeout time.Duration) bool {
	// "udp4" uses Linux unprivileged ICMP (IPPROTO_ICMP datagram socket) —
	// gated by net.ipv4.ping_group_range, open by default on Debian/Pi OS.
	// Avoids needing CAP_NET_RAW.
	conn, err := icmp.ListenPacket("udp4", "")
	if err != nil {
		return false
	}
	defer conn.Close() //nolint:errcheck // best-effort close on ICMP socket

	dst, err := net.ResolveIPAddr("ip4", addr)
	if err != nil {
		return false
	}

	seq := int(pingSeq.Add(1) & 0xffff)

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			// ID is overwritten by the kernel to the source port on "udp4";
			// reply matching is on Seq only.
			ID:   0,
			Seq:  seq,
			Data: []byte("watchdog"),
		},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		return false
	}

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return false
	}

	// On "udp4" mode, WriteTo expects a *net.UDPAddr; the port is unused.
	if _, err := conn.WriteTo(b, &net.UDPAddr{IP: dst.IP}); err != nil {
		return false
	}

	reply := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(reply)
		if err != nil {
			return false
		}
		if !peerMatches(peer, dst.IP) {
			continue
		}
		rm, err := icmp.ParseMessage(1, reply[:n])
		if err != nil {
			continue
		}
		if rm.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := rm.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		if echo.Seq == seq {
			return true
		}
	}
}

// peerMatches reports whether the source address of an ICMP reply is the host
// we sent the echo to. On "udp4" mode the peer arrives as *net.UDPAddr;
// "ip4:icmp" mode would surface *net.IPAddr.
func peerMatches(peer net.Addr, want net.IP) bool {
	switch p := peer.(type) {
	case *net.UDPAddr:
		return p.IP.Equal(want)
	case *net.IPAddr:
		return p.IP.Equal(want)
	default:
		return false
	}
}

type routeInfo struct {
	gateway    string
	iface      string
	routeIface string
}

func discoverGateway(iface string) (routeInfo, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return routeInfo{}, fmt.Errorf("read /proc/net/route: %w", err)
	}
	return parseRouteTable(string(data), iface)
}

// parseRouteTable finds the default gateway. When iface is non-empty, only
// routes on that interface (or its VLAN sub-interfaces) are considered.
// When iface is empty, default routes on any interface are considered.
// Among matching default routes, the one with the lowest metric wins —
// matching the kernel's own tie-breaker. Ties prefer the earliest entry.
func parseRouteTable(data, iface string) (routeInfo, error) {
	var best routeInfo
	bestMetric := uint64(math.MaxUint64)
	found := false

	for _, line := range strings.Split(data, "\n")[1:] {
		ri, metric, ok, err := parseRouteRow(line, iface)
		if err != nil {
			return routeInfo{}, err
		}
		if !ok || (found && metric >= bestMetric) {
			continue
		}
		best, bestMetric, found = ri, metric, true
	}

	if !found {
		if iface != "" {
			return routeInfo{}, fmt.Errorf("no default route on %s", iface)
		}
		return routeInfo{}, errors.New("no default route found")
	}
	return best, nil
}

// parseRouteRow extracts a default-route candidate from a single /proc/net/route
// line. It returns ok=false for lines that don't qualify (wrong iface, not a
// default route, missing UP|GATEWAY flags, malformed metric); it returns an
// error only for a malformed gateway in an otherwise-valid row.
func parseRouteRow(line, iface string) (routeInfo, uint64, bool, error) {
	fields := strings.Fields(line)
	if len(fields) < 8 {
		return routeInfo{}, 0, false, nil
	}
	if iface != "" && fields[0] != iface && !strings.HasPrefix(fields[0], iface+".") {
		return routeInfo{}, 0, false, nil
	}
	// Destination 00000000 = default route; flags: RTF_UP (0x1) + RTF_GATEWAY (0x2)
	if fields[1] != "00000000" {
		return routeInfo{}, 0, false, nil
	}
	flags, err := strconv.ParseUint(fields[3], 16, 16)
	if err != nil {
		return routeInfo{}, 0, false, nil //nolint:nilerr // skip malformed rows
	}
	if flags&0x3 != 0x3 {
		return routeInfo{}, 0, false, nil
	}
	metric, err := strconv.ParseUint(fields[6], 10, 32)
	if err != nil {
		return routeInfo{}, 0, false, nil //nolint:nilerr // skip malformed rows
	}
	gw, err := parseHexIP(fields[2])
	if err != nil {
		return routeInfo{}, 0, false, fmt.Errorf("parse gateway: %w", err)
	}
	ri := routeInfo{gateway: gw, routeIface: fields[0]}
	if iface != "" {
		ri.iface = iface
	} else {
		ri.iface, _, _ = strings.Cut(fields[0], ".")
	}
	return ri, metric, true, nil
}

func parseHexIP(hex string) (string, error) {
	if len(hex) != 8 {
		return "", fmt.Errorf("invalid hex IP: %s", hex)
	}
	var octets [4]uint64
	for i := range 4 {
		v, err := strconv.ParseUint(hex[i*2:i*2+2], 16, 8)
		if err != nil {
			return "", fmt.Errorf("invalid hex IP: %s", hex)
		}
		octets[i] = v
	}
	// /proc/net/route stores IPs in little-endian
	return fmt.Sprintf("%d.%d.%d.%d", octets[3], octets[2], octets[1], octets[0]), nil
}

func hasCarrier(iface string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/carrier", iface))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

func flushARPAndRoutes(ctx context.Context, iface string) error {
	if err := exec.CommandContext(ctx, "ip", "neigh", "flush", "dev", iface).Run(); err != nil {
		return fmt.Errorf("ip neigh flush: %w", err)
	}
	if err := exec.CommandContext(ctx, "ip", "route", "flush", "cache").Run(); err != nil {
		return fmt.Errorf("ip route flush cache: %w", err)
	}
	return nil
}

func restartNetworkd(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "restart", "systemd-networkd").Run()
}

func cycleInterface(ctx context.Context, iface string, downWait time.Duration) error {
	if err := exec.CommandContext(ctx, "ip", "link", "set", iface, "down").Run(); err != nil {
		return fmt.Errorf("link down: %w", err)
	}
	time.Sleep(downWait)
	if err := exec.CommandContext(ctx, "ip", "link", "set", iface, "up").Run(); err != nil {
		return fmt.Errorf("link up: %w", err)
	}
	return nil
}
