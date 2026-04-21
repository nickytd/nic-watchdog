// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
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
		name      string
		iface     string
		data      string
		wantGW    string
		wantIface string
		wantErr   bool
	}{
		{
			name:  "single default route",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:    "192.168.1.1",
			wantIface: "eth0",
		},
		{
			name:  "multiple routes picks default",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0\t00A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
				"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n",
			wantGW:    "192.168.1.1",
			wantIface: "eth0",
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
			wantGW:    "10.0.0.1",
			wantIface: "eth0",
		},
		{
			name:  "VLAN sub-interface matches base iface",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0.1\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantGW:    "192.168.0.1",
			wantIface: "eth0.1",
		},
		{
			name:  "VLAN sub-interface with higher ID",
			iface: "eth0",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0.100\t00000A0A\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
				"eth0.1\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantGW:    "192.168.0.1",
			wantIface: "eth0.1",
		},
		{
			name:  "different base iface does not match VLAN",
			iface: "eth1",
			data: "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
				"eth0.1\t00000000\t0100A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
			wantErr: true,
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
			if got.routeIface != tt.wantIface {
				t.Errorf("routeIface = %q, want %q", got.routeIface, tt.wantIface)
			}
		})
	}
}
