// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net"
	"testing"
	"time"
)

func TestParseHexIP(t *testing.T) {
	tests := []struct {
		name    string
		hex     string
		want    string
		wantErr bool
	}{
		{
			name: "typical gateway 192.168.1.1",
			hex:  "0101A8C0",
			want: "192.168.1.1",
		},
		{
			name: "loopback 127.0.0.1",
			hex:  "0100007F",
			want: "127.0.0.1",
		},
		{
			name: "all zeros",
			hex:  "00000000",
			want: "0.0.0.0",
		},
		{
			name: "all ones 255.255.255.255",
			hex:  "FFFFFFFF",
			want: "255.255.255.255",
		},
		{
			name: "10.0.0.1",
			hex:  "0100000A",
			want: "10.0.0.1",
		},
		{
			name:    "too short",
			hex:     "0101A8",
			wantErr: true,
		},
		{
			name:    "too long",
			hex:     "0101A8C0FF",
			wantErr: true,
		},
		{
			name:    "invalid hex chars",
			hex:     "ZZZZZZZZ",
			wantErr: true,
		},
		{
			name:    "empty string",
			hex:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHexIP(tt.hex)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseHexIP(%q) = %q, want error", tt.hex, got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseHexIP(%q) error = %v", tt.hex, err)
				return
			}
			if got != tt.want {
				t.Errorf("parseHexIP(%q) = %q, want %q", tt.hex, got, tt.want)
			}
		})
	}
}

func TestParseRouteTable(t *testing.T) {
	tests := []struct {
		name        string
		iface       string
		data        string
		wantGW      string
		wantIface   string
		wantRouteIf string
		wantErr     bool
	}{
		{
			name:  "single default route",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0",
		},
		{
			name:  "multiple routes picks default",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0",
		},
		{
			name:  "wrong interface ignored",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantErr: true,
		},
		{
			name:  "no default route",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t0000A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n",
			wantErr: true,
		},
		{
			name:  "route without gateway flag ignored",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00000000\t0101A8C0\t0001\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantErr: true,
		},
		{
			name:    "empty route table",
			iface:   "eth0",
			data:    "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n",
			wantErr: true,
		},
		{
			name:  "10.x gateway",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00000000\t0100000A\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:      "10.0.0.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0",
		},
		{
			name:  "VLAN sub-interface matches base iface",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0.1\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.0.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0.1",
		},
		{
			name:  "VLAN sub-interface with higher ID",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0.100\t00000A0A\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
				"eth0.1\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.0.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0.1",
		},
		{
			name:  "different base iface does not match VLAN",
			iface: "eth1",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0.1\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantErr: true,
		},
		{
			name:  "auto-detect picks first default route",
			iface: "",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"end0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.1",
			wantIface:   "end0",
			wantRouteIf: "end0",
		},
		{
			name:  "auto-detect with VLAN extracts base iface",
			iface: "",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"end0.100\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.0.1",
			wantIface:   "end0",
			wantRouteIf: "end0.100",
		},
		{
			name:  "auto-detect skips non-default routes",
			iface: "",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t0000A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
				"wlan0\t00000000\t0201A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.2",
			wantIface:   "wlan0",
			wantRouteIf: "wlan0",
		},
		{
			name:    "auto-detect empty table",
			iface:   "",
			data:    "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n",
			wantErr: true,
		},
		{
			name:  "VLAN sub-interface with lower metric beats base iface",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
				"eth0.10\t00000000\t0101A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0.10",
		},
		{
			name:  "two defaults on same iface — lower metric wins",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00000000\t0102A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0",
		},
		{
			name:  "auto-detect picks lowest-metric default across ifaces",
			iface: "",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"wlan0\t00000000\t0201A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0",
		},
		{
			name:  "tie on metric prefers earlier entry",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
				"eth0.10\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:      "192.168.1.1",
			wantIface:   "eth0",
			wantRouteIf: "eth0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRouteTable(tt.data, tt.iface)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseRouteTable() = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseRouteTable() error = %v", err)
				return
			}
			if got.gateway != tt.wantGW {
				t.Errorf("gateway = %q, want %q", got.gateway, tt.wantGW)
			}
			if got.iface != tt.wantIface {
				t.Errorf("iface = %q, want %q", got.iface, tt.wantIface)
			}
			if got.routeIface != tt.wantRouteIf {
				t.Errorf("routeIface = %q, want %q", got.routeIface, tt.wantRouteIf)
			}
		})
	}
}

func TestPeerMatches(t *testing.T) {
	tests := []struct {
		name string
		peer net.Addr
		want net.IP
		ok   bool
	}{
		{
			name: "udp peer matches",
			peer: &net.UDPAddr{IP: net.ParseIP("8.8.8.8")},
			want: net.ParseIP("8.8.8.8"),
			ok:   true,
		},
		{
			name: "udp peer mismatch",
			peer: &net.UDPAddr{IP: net.ParseIP("8.8.4.4")},
			want: net.ParseIP("8.8.8.8"),
			ok:   false,
		},
		{
			name: "ip peer matches (raw socket fallback)",
			peer: &net.IPAddr{IP: net.ParseIP("192.168.1.1")},
			want: net.ParseIP("192.168.1.1"),
			ok:   true,
		},
		{
			name: "ip peer mismatch",
			peer: &net.IPAddr{IP: net.ParseIP("192.168.1.2")},
			want: net.ParseIP("192.168.1.1"),
			ok:   false,
		},
		{
			name: "ipv4-mapped ipv6 equals ipv4",
			peer: &net.UDPAddr{IP: net.ParseIP("::ffff:8.8.8.8")},
			want: net.ParseIP("8.8.8.8"),
			ok:   true,
		},
		{
			name: "unknown peer type rejected",
			peer: &net.TCPAddr{IP: net.ParseIP("8.8.8.8")},
			want: net.ParseIP("8.8.8.8"),
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerMatches(tt.peer, tt.want); got != tt.ok {
				t.Errorf("peerMatches(%v, %v) = %v, want %v", tt.peer, tt.want, got, tt.ok)
			}
		})
	}
}

func TestPingSeqIsUnique(t *testing.T) {
	// Each ping must use a fresh Seq so the read loop can't accept a stale or
	// concurrent reply. Sample many calls; any duplicate would be a regression.
	const n = 1000
	seen := make(map[int]struct{}, n)
	for range n {
		s := int(pingSeq.Add(1) & 0xffff)
		if _, dup := seen[s]; dup {
			// 16-bit space wraps every 65536; with n=1000 starting fresh per
			// process this is effectively impossible.
			t.Fatalf("duplicate seq %d after %d iterations", s, len(seen))
		}
		seen[s] = struct{}{}
	}
}

// TestPingLoopbackTimeout verifies ping returns false when the target is
// unreachable, and exercises the unprivileged ICMP socket path. Skipped when
// the kernel rejects unprivileged ICMP (closed ping_group_range, non-Linux
// CI, or sandbox).
func TestPingLoopbackTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("requires unprivileged ICMP socket")
	}

	ctx := t.Context()

	// 192.0.2.1 is in TEST-NET-1 (RFC 5737) — guaranteed unreachable. We use a
	// short timeout because we don't want to wait long for the negative case.
	start := time.Now()
	ok := ping(ctx, "192.0.2.1", 200*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Errorf("ping to TEST-NET-1 unexpectedly succeeded")
	}
	// Loose upper bound — the call must respect its deadline, not block for
	// kernel default ICMP timeouts.
	if elapsed > 2*time.Second {
		t.Errorf("ping took %v, expected < 2s (deadline ignored?)", elapsed)
	}
}
