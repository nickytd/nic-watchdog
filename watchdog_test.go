// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fakeRecoverer records each call site so tests can assert which step the
// state machine took. Per-call errors can be injected via the *Err fields.
type fakeRecoverer struct {
	flushCalls    int
	restartCalls  int
	cycleCalls    int
	flushErr      error
	restartErr    error
	cycleErr      error
	flushIface    string
	cycleIface    string
	cycleDownWait time.Duration
}

func (f *fakeRecoverer) flush(_ context.Context, iface string) error {
	f.flushCalls++
	f.flushIface = iface
	return f.flushErr
}

func (f *fakeRecoverer) restartNetworkd(_ context.Context) error {
	f.restartCalls++
	return f.restartErr
}

func (f *fakeRecoverer) cycle(_ context.Context, iface string, downWait time.Duration) error {
	f.cycleCalls++
	f.cycleIface = iface
	f.cycleDownWait = downWait
	return f.cycleErr
}

// silentLogger discards output so tests don't leak log lines into go test -v.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewWatchdog(t *testing.T) {
	cfg := Config{
		Iface:         "eth1",
		PingTarget:    "1.1.1.1",
		Gateway:       "10.0.0.1",
		CheckInterval: 5 * time.Second,
		Cooldown:      3 * time.Minute,
		SoftMax:       5,
	}

	w := NewWatchdog(cfg, silentLogger())

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
	if w.rec == nil {
		t.Error("rec is nil; expected default osRecoverer")
	}
}

// TestSoftRecoverDispatch exercises the actual softRecover state machine
// against a fake recoverer — the previous version of this test re-implemented
// the switch in the test body, which would stay green even if softRecover
// were deleted.
func TestSoftRecoverDispatch(t *testing.T) {
	tests := []struct {
		name         string
		softCount    int
		softMax      int
		wantFlush    int
		wantRestart  int
		wantCycle    int
		wantSoftZero bool
	}{
		{name: "first attempt flushes ARP", softCount: 1, softMax: 3, wantFlush: 1},
		{name: "second attempt restarts networkd", softCount: 2, softMax: 3, wantRestart: 1},
		{name: "at softMax escalates to full cycle", softCount: 3, softMax: 3, wantCycle: 1, wantSoftZero: true},
		{name: "above softMax also escalates", softCount: 5, softMax: 3, wantCycle: 1, wantSoftZero: true},
		{name: "zero softCount is a no-op", softCount: 0, softMax: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRecoverer{}
			w := &Watchdog{
				iface:        "eth0",
				routeIface:   "eth0",
				gateway:      "10.0.0.1",
				pingTarget:   "192.0.2.1",
				cooldown:     time.Millisecond,
				softMax:      tt.softMax,
				linkDownWait: time.Millisecond,
				rec:          fake,
				log:          silentLogger(),
				softCount:    tt.softCount,
			}

			// Pre-cancelled ctx — sleepCtx in fullCycle returns immediately,
			// so escalating cases don't wait the 3s post-cycle settle.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			w.softRecover(ctx)

			if fake.flushCalls != tt.wantFlush {
				t.Errorf("flush calls = %d, want %d", fake.flushCalls, tt.wantFlush)
			}
			if fake.restartCalls != tt.wantRestart {
				t.Errorf("restart calls = %d, want %d", fake.restartCalls, tt.wantRestart)
			}
			if fake.cycleCalls != tt.wantCycle {
				t.Errorf("cycle calls = %d, want %d", fake.cycleCalls, tt.wantCycle)
			}
			if tt.wantSoftZero && w.softCount != 0 {
				t.Errorf("softCount = %d, want 0 after escalation", w.softCount)
			}
		})
	}
}

func TestFullCycleCooldown(t *testing.T) {
	fake := &fakeRecoverer{}
	w := &Watchdog{
		iface:        "eth0",
		cooldown:     10 * time.Minute,
		linkDownWait: time.Millisecond,
		rec:          fake,
		log:          silentLogger(),
		lastCycle:    time.Now(), // cycle just ran; cooldown is active
	}

	w.fullCycle(context.Background())

	if fake.cycleCalls != 0 {
		t.Errorf("cycle was called during cooldown (calls=%d)", fake.cycleCalls)
	}
}

func TestFullCycleAllowsAfterCooldown(t *testing.T) {
	fake := &fakeRecoverer{}
	w := &Watchdog{
		iface:        "eth0",
		gateway:      "10.0.0.1",
		pingTarget:   "192.0.2.1", // unreachable, post-cycle ping returns false
		cooldown:     10 * time.Millisecond,
		linkDownWait: time.Millisecond,
		rec:          fake,
		log:          silentLogger(),
		lastCycle:    time.Now().Add(-time.Hour), // cooldown elapsed long ago
	}

	// Pre-cancelled ctx so the post-cycle settle doesn't run for 3s.
	// rec.cycle is a fake and ignores ctx, so it still records the call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w.fullCycle(ctx)

	if fake.cycleCalls != 1 {
		t.Errorf("cycle calls = %d, want 1", fake.cycleCalls)
	}
	if fake.cycleIface != "eth0" {
		t.Errorf("cycle iface = %q, want eth0", fake.cycleIface)
	}
}

func TestFullCycleCycleErrorRecorded(t *testing.T) {
	fake := &fakeRecoverer{cycleErr: errors.New("boom")}
	w := &Watchdog{
		iface:        "eth0",
		linkDownWait: time.Millisecond,
		rec:          fake,
		log:          silentLogger(),
	}

	w.fullCycle(t.Context())

	if fake.cycleCalls != 1 {
		t.Errorf("cycle calls = %d, want 1", fake.cycleCalls)
	}
}

// TestFullCycleHonorsContextDuringSettle verifies #4: the post-cycle settle
// must return promptly when the context is cancelled, instead of blocking
// for the full 3 s on shutdown.
func TestFullCycleHonorsContextDuringSettle(t *testing.T) {
	fake := &fakeRecoverer{}
	w := &Watchdog{
		iface:        "eth0",
		gateway:      "10.0.0.1",
		pingTarget:   "192.0.2.1",
		cooldown:     time.Millisecond,
		linkDownWait: time.Millisecond,
		rec:          fake,
		log:          silentLogger(),
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel before we even start

	start := time.Now()
	w.fullCycle(ctx)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("fullCycle took %v after pre-cancelled ctx, expected near-immediate return", elapsed)
	}
}
