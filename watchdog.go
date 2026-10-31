// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Post-cycle gating timings. waitForCarrier blocks on L1/L2 up to the carrier
// timeout (a ceiling, not a tuned sleep — it returns the instant carrier is
// up), polling at the carrier poll interval. Once carrier is up, the L3 probe
// retries for postCycleL3Budget, probing every postCycleL3Interval, to absorb
// ARP/neighbor warm-up and switch MAC-learning delay. Each probe's own timeout
// is capped at postCycleL3ProbeTimeout and further shrunk to the budget left,
// so the retry loop can't overshoot postCycleL3Budget by a full probe.
const (
	defaultCarrierTimeout   = 20 * time.Second
	defaultCarrierPoll      = 250 * time.Millisecond
	postCycleL3Budget       = 5 * time.Second
	postCycleL3Interval     = 1 * time.Second
	postCycleL3ProbeTimeout = 2 * time.Second
)

// Carrier debounce timings for the both-down gate in check(). Before
// suppressing a cycle on a healthy link, carrier is sampled up to
// carrierDebounceSamples times at carrierDebounceInterval. The sampling
// short-circuits on the first down read, so this latency (worst case
// ~(samples-1) * interval) is only paid when the link is genuinely up — the
// case where we would suppress. A flapping PHY (the BCM link-drop bug, which
// bounces carrier) yields at least one down sample and still cycles.
const (
	carrierDebounceSamples  = 3
	carrierDebounceInterval = 200 * time.Millisecond
)

// errCarrierTimeout signals waitForCarrier gave up before the PHY reported
// carrier — a genuine "link never came back" failure, distinct from an L3 one.
var errCarrierTimeout = errors.New("carrier did not come up before timeout")

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
	iface          string
	routeIface     string
	pingTarget     string
	gateway        string
	checkInterval  time.Duration
	cooldown       time.Duration
	softMax        int
	linkDownWait   time.Duration
	carrierTimeout time.Duration
	carrierPoll    time.Duration
	metricsAddr    string

	rec       recoverer
	carrierFn func(iface string) bool
	m         *metrics

	softCount int
	lastCycle time.Time
	log       *slog.Logger
}

func NewWatchdog(cfg Config, logger *slog.Logger) *Watchdog {
	return &Watchdog{
		iface:          cfg.Iface,
		pingTarget:     cfg.PingTarget,
		gateway:        cfg.Gateway,
		checkInterval:  cfg.CheckInterval,
		cooldown:       cfg.Cooldown,
		softMax:        cfg.SoftMax,
		linkDownWait:   5 * time.Second,
		carrierTimeout: defaultCarrierTimeout,
		carrierPoll:    defaultCarrierPoll,
		metricsAddr:    cfg.MetricsAddr,
		rec:            osRecoverer{},
		carrierFn:      hasCarrier,
		m:              newMetrics(),
		log:            logger,
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
			slog.String(roleGateway, w.gateway),
			slog.String("iface", w.iface),
			slog.String("routeIface", w.routeIface),
		)
	}

	if w.routeIface == "" {
		w.routeIface = w.iface
	}

	w.m.info.WithLabelValues(w.iface, w.routeIface).Set(1)

	w.log.Info("watchdog started",
		slog.String("iface", w.iface),
		slog.String("routeIface", w.routeIface),
		slog.String(roleTarget, w.pingTarget),
		slog.String(roleGateway, w.gateway),
		slog.Duration("interval", w.checkInterval),
	)

	if w.metricsAddr != "" {
		ms := newMetricsServer(w.metricsAddr, w.m.registry, w.log)
		go func() {
			if err := ms.run(ctx); err != nil {
				w.log.Error("metrics server stopped with error",
					slog.String("addr", w.metricsAddr),
					slog.String("error", err.Error()),
				)
			}
		}()
	}

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

// pingObs wraps ping with histogram + gauge instrumentation. The role label
// (roleTarget or roleGateway) makes both signals queryable separately even
// when pingTarget == gateway in pathological configs.
func (w *Watchdog) pingObs(ctx context.Context, role, addr string, timeout time.Duration) bool {
	start := time.Now()
	ok := ping(ctx, addr, timeout)
	result := "fail"
	if ok {
		result = "ok"
	}
	w.m.pingDurationSeconds.WithLabelValues(role, result).Observe(time.Since(start).Seconds())
	switch role {
	case roleTarget:
		w.m.externalReachable.WithLabelValues(addr).Set(gauge01(ok))
	case roleGateway:
		w.m.gatewayReachable.WithLabelValues(addr).Set(gauge01(ok))
	}
	return ok
}

func (w *Watchdog) check(ctx context.Context) {
	if w.pingObs(ctx, roleTarget, w.pingTarget, 2*time.Second) {
		w.m.checkTotal.WithLabelValues("ok").Inc()
		if w.softCount > 0 {
			w.log.Info("external connectivity restored", slog.Int("after_soft_attempts", w.softCount))
		}
		w.softCount = 0
		w.m.softAttempts.Set(0)
		return
	}

	gatewayUp := w.pingObs(ctx, roleGateway, w.gateway, 2*time.Second)

	if gatewayUp {
		w.m.checkTotal.WithLabelValues("external_down").Inc()
		w.softCount++
		w.m.softAttempts.Set(float64(w.softCount))
		w.softRecover(ctx)
		return
	}

	// Target and gateway are both unreachable. Whether a full cycle can help
	// depends entirely on L1/L2: a cycle only recovers a PHY link-drop (the BCM
	// bug this daemon exists for, which drops carrier). If carrier is up, the
	// link is healthy and the outage is remote — cycling a healthy NIC there is
	// all cost (it can sever in-flight TCP sessions, e.g. a krbd mount, turning
	// a transient remote blip into a sticky emergency_ro filesystem). Gate the
	// cycle on carrier being down, read with a debounce so a flapping PHY still
	// cycles.
	if w.carrierDebouncedUp(ctx) {
		// Link is healthy at L1/L2. One optional 5s gateway retry to catch a
		// slow neighbor, then bail to the soft/backoff path — never cycle.
		if w.pingObs(ctx, roleGateway, w.gateway, 5*time.Second) {
			w.m.checkTotal.WithLabelValues("gateway_recovered_on_retry").Inc()
			w.log.Info("gateway reachable on retry", slog.String(roleGateway, w.gateway))
			return
		}
		w.m.checkTotal.WithLabelValues("remote_down_carrier_up").Inc()
		w.log.Warn("target and gateway unreachable but carrier is up — remote outage, suppressing interface cycle",
			slog.String(roleTarget, w.pingTarget),
			slog.String(roleGateway, w.gateway),
		)
		return
	}

	// Carrier is down — the L1/L2 PHY fault the cycle exists to fix.
	w.m.checkTotal.WithLabelValues("carrier_down").Inc()
	w.fullCycle(ctx)
}

// carrierDebouncedUp samples carrierFn up to carrierDebounceSamples times at
// carrierDebounceInterval and reports whether every sample observed carrier.
// It short-circuits to false on the first sample that reads down, so a
// genuinely healthy link is the only thing that returns true; a flapping PHY
// (carrier bouncing up/down) yields a down sample and returns false, keeping
// the cycle available for the link-drop bug recovery depends on. The carrier_up
// gauge is updated from each read. If ctx is canceled mid-sleep the loop stops
// early and returns the most recent observation — a canceled run is shutting
// down and the result does not drive a cycle (check() is not re-entered).
func (w *Watchdog) carrierDebouncedUp(ctx context.Context) bool {
	up := w.carrierFn(w.iface)
	w.m.carrierUp.WithLabelValues(w.iface).Set(gauge01(up))
	if !up {
		return false
	}
	for range carrierDebounceSamples - 1 {
		if err := sleepCtx(ctx, carrierDebounceInterval); err != nil {
			return up
		}
		up = w.carrierFn(w.iface)
		w.m.carrierUp.WithLabelValues(w.iface).Set(gauge01(up))
		if !up {
			return false
		}
	}
	return up
}

func (w *Watchdog) softRecover(ctx context.Context) {
	switch {
	case w.softCount == 1:
		w.log.Info("external unreachable, gateway up — flushing ARP and routes",
			slog.String(roleGateway, w.gateway),
			slog.Int("attempt", w.softCount),
		)
		w.m.recoveryTotal.WithLabelValues("flush").Inc()
		if err := w.rec.flush(ctx, w.routeIface); err != nil {
			w.m.recoveryFailureTot.WithLabelValues("flush").Inc()
			w.log.Error("flush failed", slog.String("error", err.Error()))
		}

	case w.softCount == 2:
		w.log.Info("external still unreachable — restarting systemd-networkd",
			slog.Int("attempt", w.softCount),
		)
		w.m.recoveryTotal.WithLabelValues("networkd_restart").Inc()
		if err := w.rec.restartNetworkd(ctx); err != nil {
			w.m.recoveryFailureTot.WithLabelValues("networkd_restart").Inc()
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

// waitForCarrier blocks until carrierFn reports the interface has carrier, the
// timeout elapses, or ctx is canceled. It checks once immediately (fast path
// for an already-up link) then polls every poll. The elapsed wait is observed
// into carrierWaitSeconds on every return path and the carrier_up gauge is
// updated. Returns nil on carrier up, errCarrierTimeout on timeout, or
// ctx.Err() on cancellation.
//
// A non-positive timeout or poll falls back to the package defaults, so a
// Watchdog built without seeding these fields can't drive time.NewTimer/
// time.NewTicker with a zero interval (which would panic).
func (w *Watchdog) waitForCarrier(ctx context.Context, iface string, timeout, poll time.Duration) error {
	if timeout <= 0 {
		timeout = defaultCarrierTimeout
	}
	if poll <= 0 {
		poll = defaultCarrierPoll
	}

	start := time.Now()
	defer func() {
		w.m.carrierWaitSeconds.Observe(time.Since(start).Seconds())
	}()

	check := func() bool {
		up := w.carrierFn(iface)
		w.m.carrierUp.WithLabelValues(iface).Set(gauge01(up))
		return up
	}

	if check() {
		return nil
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errCarrierTimeout
		case <-ticker.C:
			if check() {
				return nil
			}
		}
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
		slog.String(roleGateway, w.gateway),
		slog.String(roleTarget, w.pingTarget),
	)
	w.lastCycle = time.Now()
	w.m.lastCycleTimestamp.Set(float64(w.lastCycle.Unix()))
	w.m.recoveryTotal.WithLabelValues("cycle").Inc()

	if err := w.rec.cycle(ctx, w.iface, w.linkDownWait); err != nil {
		w.m.recoveryFailureTot.WithLabelValues("cycle").Inc()
		w.log.Error("cycle failed", slog.String("error", err.Error()))
		return
	}

	if err := w.waitForCarrier(ctx, w.iface, w.carrierTimeout, w.carrierPoll); err != nil {
		switch {
		case ctx.Err() != nil:
			w.log.Info("shutdown during post-cycle carrier wait", slog.String("error", err.Error()))
		case errors.Is(err, errCarrierTimeout):
			// Link never came back — a genuine L1/L2 failure, distinct from an
			// L3 one. Do NOT reset softCount: the interface is still bad, so the
			// next tick must keep escalating (same invariant as softRecover).
			w.m.checkTotal.WithLabelValues("link_down_after_cycle").Inc()
			w.log.Error("link failed to come up after cycle",
				slog.Duration("timeout", w.carrierTimeout),
			)
		default:
			w.log.Error("post-cycle carrier wait failed", slog.String("error", err.Error()))
		}
		return
	}

	// Carrier is provably up. Retry L3 briefly to absorb ARP/neighbor warm-up
	// and the switch's MAC-learning delay before declaring an L3 failure. The
	// per-probe timeout is capped to the budget left so the whole loop honors
	// postCycleL3Budget as a real ceiling rather than overshooting by one or
	// two full probe timeouts.
	deadline := time.Now().Add(postCycleL3Budget)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		probeTimeout := min(remaining, postCycleL3ProbeTimeout)

		if w.pingObs(ctx, roleTarget, w.pingTarget, probeTimeout) {
			w.log.Info("connectivity restored after cycle")
			w.softCount = 0
			w.m.softAttempts.Set(0)
			return
		}

		remaining = time.Until(deadline)
		if remaining <= 0 {
			break
		}
		probeTimeout = min(remaining, postCycleL3ProbeTimeout)

		if w.pingObs(ctx, roleGateway, w.gateway, probeTimeout) {
			w.log.Warn("gateway reachable after cycle, external still down — soft recovery will continue")
			return
		}
		if time.Now().After(deadline) {
			break
		}
		if err := sleepCtx(ctx, postCycleL3Interval); err != nil {
			w.log.Info("shutdown during post-cycle L3 retry", slog.String("error", err.Error()))
			return
		}
	}

	w.log.Error("network still unreachable after cycle")
}
