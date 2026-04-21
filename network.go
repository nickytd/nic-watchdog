// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func ping(ctx context.Context, addr string, timeout time.Duration) bool {
	conn, err := icmp.ListenPacket("ip4:icmp", "")
	if err != nil {
		return false
	}
	defer conn.Close() //nolint:errcheck // best-effort close on ICMP socket

	dst, err := net.ResolveIPAddr("ip4", addr)
	if err != nil {
		return false
	}

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   os.Getpid() & 0xffff,
			Seq:  1,
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

	if _, err := conn.WriteTo(b, dst); err != nil {
		return false
	}

	reply := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(reply)
		if err != nil {
			return false
		}
		rm, err := icmp.ParseMessage(1, reply[:n])
		if err != nil {
			return false
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			return true
		}
	}
}

type routeInfo struct {
	gateway    string
	routeIface string
}

func discoverGateway(iface string) (routeInfo, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return routeInfo{}, fmt.Errorf("read /proc/net/route: %w", err)
	}
	return parseRouteTable(string(data), iface)
}

// parseRouteTable finds the default gateway on iface or any VLAN sub-interface (iface.NNN).
func parseRouteTable(data, iface string) (routeInfo, error) {
	prefix := iface + "."
	for _, line := range strings.Split(data, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[0] != iface && !strings.HasPrefix(fields[0], prefix) {
			continue
		}
		// Destination 00000000 = default route; flags: RTF_UP (0x1) + RTF_GATEWAY (0x2)
		if fields[1] == "00000000" {
			flags, err := strconv.ParseUint(fields[3], 16, 16)
			if err != nil || flags&0x3 != 0x3 {
				continue
			}
			gw, err := parseHexIP(fields[2])
			if err != nil {
				return routeInfo{}, fmt.Errorf("parse gateway: %w", err)
			}
			return routeInfo{gateway: gw, routeIface: fields[0]}, nil
		}
	}
	return routeInfo{}, fmt.Errorf("no default route on %s", iface)
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
