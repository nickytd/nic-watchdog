// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freeAddr finds an unused TCP port on loopback so tests don't collide with
// well-known exporter ports. Closes the listener before returning the address.
func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestMetricsServerServesScrapeAndShutsDown(t *testing.T) {
	w := NewWatchdog(Config{
		PingTarget:    testTargetUnreachable,
		CheckInterval: time.Second,
		Cooldown:      time.Second,
		SoftMax:       3,
	}, silentLogger())
	// Bump a couple of counters so the scrape body has signal we can grep for.
	w.m.recoveryTotal.WithLabelValues("flush").Inc()
	w.m.softAttempts.Set(2)

	addr := freeAddr(t)
	ms := newMetricsServer(addr, w.m.registry, silentLogger())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ms.run(ctx) }()

	// Poll until the server is actually accepting connections.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	var err error
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", nil)
		if reqErr != nil {
			t.Fatalf("build request: %v", reqErr)
		}
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("scrape failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyStr := string(body)

	wantSubstrings := []string{
		`nic_watchdog_recovery_total{step="flush"} 1`,
		`nic_watchdog_soft_attempts 2`,
		// Free Go runtime metric proves the runtime collectors are wired.
		`go_goroutines`,
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(bodyStr, s) {
			t.Errorf("scrape body missing %q", s)
		}
	}

	// Cancel and require the run loop to return within the shutdown grace.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metrics server did not shut down within 5s of ctx cancel")
	}
}

// TestMetricsServerBindFailureSurfaces verifies that a port already in use
// produces a non-nil error from run, instead of failing silently at scrape
// time. The Watchdog wraps this error in a logged Error event and keeps
// running — the daemon must not die because of metrics, but the operator
// must see why scraping doesn't work.
func TestMetricsServerBindFailureSurfaces(t *testing.T) {
	// Hold a listener so the new server can't bind the same address.
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close() //nolint:errcheck
	addr := l.Addr().String()

	w := NewWatchdog(Config{
		PingTarget:    testTargetUnreachable,
		CheckInterval: time.Second,
		Cooldown:      time.Second,
		SoftMax:       3,
	}, silentLogger())

	ms := newMetricsServer(addr, w.m.registry, silentLogger())

	err = ms.run(t.Context())
	if err == nil {
		t.Errorf("run returned nil; want bind error for already-used %s", addr)
	}
}
