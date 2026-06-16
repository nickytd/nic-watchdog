// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"time"
)

// recoverer performs the OS-level recovery actions. The default implementation
// (osRecoverer) shells out to ip / systemctl; tests substitute a fake to
// observe the state machine without touching the host.
type recoverer interface {
	flush(ctx context.Context, iface string) error
	restartNetworkd(ctx context.Context) error
	cycle(ctx context.Context, iface string, downWait time.Duration) error
}

type osRecoverer struct{}

func (osRecoverer) flush(ctx context.Context, iface string) error {
	return flushARPAndRoutes(ctx, iface)
}

func (osRecoverer) restartNetworkd(ctx context.Context) error {
	return restartNetworkd(ctx)
}

func (osRecoverer) cycle(ctx context.Context, iface string, downWait time.Duration) error {
	return cycleInterface(ctx, iface, downWait)
}

type Watchdog struct {
	iface         string
	routeIface    string
	pingTarget    string
	gateway       string
	checkInterval time.Duration
	cooldown      time.Duration
	softMax       int
	linkDownWait  time.Duration

	rec recoverer

	softCount int
	lastCycle time.Time
	log       *slog.Logger
}

func NewWatchdog(cfg Config, logger *slog.Logger) *Watchdog {
	return &Watchdog{
		iface:         cfg.Iface,
		pingTarget:    cfg.PingTarget,
		gateway:       cfg.Gateway,
		checkInterval: cfg.CheckInterval,
		cooldown:      cfg.Cooldown,
		softMax:       cfg.SoftMax,
		linkDownWait:  5 * time.Second,
		rec:           osRecoverer{},
		log:           logger,
	}
}

func (w *Watchdog) Run(ctx context.Context) error {
	if w.gateway == "" {
		ri, err := discoverGateway(w.iface)
		if err != nil {
			return err
		}
		w.gateway = ri.gateway
		w.routeIface = ri.routeIface
		if w.iface == "" {
			w.iface = ri.iface
		}
		w.log.Info("discovered gateway",
			slog.String("gateway", w.gateway),
			slog.String("iface", w.iface),
			slog.String("routeIface", w.routeIface),
		)
	}

	if w.routeIface == "" {
		w.routeIface = w.iface
	}

	w.log.Info("watchdog started",
		slog.String("iface", w.iface),
		slog.String("routeIface", w.routeIface),
		slog.String("target", w.pingTarget),
		slog.String("gateway", w.gateway),
		slog.Duration("interval", w.checkInterval),
	)

	ticker := time.NewTicker(w.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.log.Info("shutting down")
			return nil
		case <-ticker.C:
			w.check(ctx)
		}
	}
}

func (w *Watchdog) check(ctx context.Context) {
	if ping(ctx, w.pingTarget, 2*time.Second) {
		if w.softCount > 0 {
			w.log.Info("external connectivity restored", slog.Int("after_soft_attempts", w.softCount))
		}
		w.softCount = 0
		return
	}

	gatewayUp := ping(ctx, w.gateway, 2*time.Second)

	if gatewayUp {
		w.softCount++
		w.softRecover(ctx)
		return
	}

	if hasCarrier(w.iface) {
		if ping(ctx, w.gateway, 5*time.Second) {
			w.log.Info("gateway reachable on retry", slog.String("gateway", w.gateway))
			return
		}
	}

	w.fullCycle(ctx)
}

func (w *Watchdog) softRecover(ctx context.Context) {
	switch {
	case w.softCount == 1:
		w.log.Info("external unreachable, gateway up — flushing ARP and routes",
			slog.String("gateway", w.gateway),
			slog.Int("attempt", w.softCount),
		)
		if err := w.rec.flush(ctx, w.routeIface); err != nil {
			w.log.Error("flush failed", slog.String("error", err.Error()))
		}

	case w.softCount == 2:
		w.log.Info("external still unreachable — restarting systemd-networkd",
			slog.Int("attempt", w.softCount),
		)
		if err := w.rec.restartNetworkd(ctx); err != nil {
			w.log.Error("networkd restart failed", slog.String("error", err.Error()))
		}

	case w.softCount >= w.softMax:
		w.log.Info("soft recovery exhausted — escalating to full cycle",
			slog.Int("attempts", w.softCount),
		)
		// Don't reset softCount here: if fullCycle is suppressed by cooldown
		// (or runs but doesn't restore connectivity), keeping softCount at
		// softMax means the next tick re-enters this branch and calls
		// fullCycle again, instead of restarting flush/networkd-restart from
		// attempt 1 — which would never reach a cycle while cooldown holds.
		// fullCycle resets softCount on the success path.
		w.fullCycle(ctx)
	}
}

func (w *Watchdog) fullCycle(ctx context.Context) {
	elapsed := time.Since(w.lastCycle)
	if !w.lastCycle.IsZero() && elapsed < w.cooldown {
		w.log.Warn("cooldown active, skipping cycle",
			slog.Duration("elapsed", elapsed),
			slog.Duration("cooldown", w.cooldown),
		)
		return
	}

	w.log.Info("cycling interface",
		slog.String("iface", w.iface),
		slog.String("gateway", w.gateway),
		slog.String("target", w.pingTarget),
	)
	w.lastCycle = time.Now()

	if err := w.rec.cycle(ctx, w.iface, w.linkDownWait); err != nil {
		w.log.Error("cycle failed", slog.String("error", err.Error()))
		return
	}

	if err := sleepCtx(ctx, 3*time.Second); err != nil {
		w.log.Info("shutdown during post-cycle settle", slog.String("error", err.Error()))
		return
	}

	switch {
	case ping(ctx, w.pingTarget, 3*time.Second):
		w.log.Info("connectivity restored after cycle")
		w.softCount = 0
	case ping(ctx, w.gateway, 3*time.Second):
		w.log.Warn("gateway reachable after cycle, external still down — soft recovery will continue")
	default:
		w.log.Error("network still unreachable after cycle")
	}
}
