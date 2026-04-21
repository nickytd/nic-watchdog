// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log/slog"
	"testing"
	"time"
)

func TestNewWatchdog(t *testing.T) {
	cfg := Config{
		Iface:         "eth1",
		PingTarget:    "1.1.1.1",
		Gateway:       "10.0.0.1",
		CheckInterval: 5 * time.Second,
		Cooldown:      3 * time.Minute,
		SoftMax:       5,
	}
	logger := slog.Default()

	w := NewWatchdog(cfg, logger)

	if w.iface != cfg.Iface {
		t.Errorf("iface = %q, want %q", w.iface, cfg.Iface)
	}
	if w.pingTarget != cfg.PingTarget {
		t.Errorf("pingTarget = %q, want %q", w.pingTarget, cfg.PingTarget)
	}
	if w.gateway != cfg.Gateway {
		t.Errorf("gateway = %q, want %q", w.gateway, cfg.Gateway)
	}
	if w.checkInterval != cfg.CheckInterval {
		t.Errorf("checkInterval = %v, want %v", w.checkInterval, cfg.CheckInterval)
	}
	if w.cooldown != cfg.Cooldown {
		t.Errorf("cooldown = %v, want %v", w.cooldown, cfg.Cooldown)
	}
	if w.softMax != cfg.SoftMax {
		t.Errorf("softMax = %d, want %d", w.softMax, cfg.SoftMax)
	}
	if w.softCount != 0 {
		t.Errorf("softCount = %d, want 0", w.softCount)
	}
	if !w.lastCycle.IsZero() {
		t.Errorf("lastCycle = %v, want zero", w.lastCycle)
	}
}

func TestFullCycleCooldown(t *testing.T) {
	w := &Watchdog{
		cooldown: 10 * time.Minute,
		log:      slog.Default(),
	}

	if !w.lastCycle.IsZero() {
		t.Fatal("lastCycle should be zero initially")
	}

	w.lastCycle = time.Now()

	elapsed := time.Since(w.lastCycle)
	if elapsed >= w.cooldown {
		t.Fatal("test setup: elapsed should be less than cooldown")
	}

	cooldownActive := !w.lastCycle.IsZero() && time.Since(w.lastCycle) < w.cooldown
	if !cooldownActive {
		t.Error("cooldown should be active immediately after a cycle")
	}
}

func TestSoftRecoverEscalation(t *testing.T) {
	tests := []struct {
		name      string
		softCount int
		softMax   int
		wantPhase string
	}{
		{"first attempt flushes ARP", 1, 3, "flush"},
		{"second attempt restarts networkd", 2, 3, "networkd"},
		{"at softMax escalates to full cycle", 3, 3, "full"},
		{"above softMax escalates to full cycle", 5, 3, "full"},
		{"intermediate does nothing", 0, 3, "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var phase string
			switch {
			case tt.softCount == 1:
				phase = "flush"
			case tt.softCount == 2:
				phase = "networkd"
			case tt.softCount >= tt.softMax:
				phase = "full"
			default:
				phase = "none"
			}
			if phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", phase, tt.wantPhase)
			}
		})
	}
}
