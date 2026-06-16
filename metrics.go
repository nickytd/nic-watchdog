// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Probe role labels — used as both metric labels and slog field keys so a
// single change keeps dashboards, alerts, and journal logs aligned.
const (
	roleTarget  = "target"
	roleGateway = "gateway"
)

// metrics is the watchdog's instrumentation surface. All collectors are
// constructed against a private registry so callers can decide whether to
// expose them; the registry is exported via metrics.registry.
//
// Naming follows the Prometheus best-practice — base unit suffixes (_total,
// _seconds), small label cardinality, and a single _info gauge for static
// build/runtime context.
type metrics struct {
	registry *prometheus.Registry

	externalReachable   *prometheus.GaugeVec
	gatewayReachable    *prometheus.GaugeVec
	carrierUp           *prometheus.GaugeVec
	checkTotal          *prometheus.CounterVec
	recoveryTotal       *prometheus.CounterVec
	recoveryFailureTot  *prometheus.CounterVec
	softAttempts        prometheus.Gauge
	lastCycleTimestamp  prometheus.Gauge
	pingDurationSeconds *prometheus.HistogramVec
	info                *prometheus.GaugeVec
}

func newMetrics() *metrics {
	reg := prometheus.NewRegistry()

	m := &metrics{
		registry: reg,

		externalReachable: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nic_watchdog_external_reachable",
			Help: "1 when the external probe target is reachable, 0 otherwise.",
		}, []string{roleTarget}),

		gatewayReachable: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nic_watchdog_gateway_reachable",
			Help: "1 when the configured gateway is reachable, 0 otherwise. " +
				"Combined with external_reachable distinguishes link-down from PHY TX failures.",
		}, []string{roleGateway}),

		carrierUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nic_watchdog_carrier_up",
			Help: "1 when the kernel reports carrier on the monitored interface, 0 otherwise.",
		}, []string{"iface"}),

		checkTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nic_watchdog_check_total",
			Help: "Number of liveness checks broken down by classification.",
		}, []string{"result"}),

		recoveryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nic_watchdog_recovery_total",
			Help: "Number of recovery actions invoked, by step.",
		}, []string{"step"}),

		recoveryFailureTot: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nic_watchdog_recovery_failure_total",
			Help: "Number of recovery actions that returned an error, by step.",
		}, []string{"step"}),

		softAttempts: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nic_watchdog_soft_attempts",
			Help: "Current number of consecutive soft-recovery attempts since the last successful external ping.",
		}),

		lastCycleTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nic_watchdog_last_cycle_timestamp_seconds",
			Help: "Unix time of the most recent full interface cycle. 0 if no cycle has occurred.",
		}),

		pingDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "nic_watchdog_ping_duration_seconds",
			Help:    "ICMP ping duration. Spikes correlate with PHY soft failures.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}, []string{roleTarget, "result"}),

		info: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nic_watchdog_info",
			Help: "Static build/runtime info. Always 1; use the labels.",
		}, []string{"iface", "route_iface"}),
	}

	reg.MustRegister(
		m.externalReachable,
		m.gatewayReachable,
		m.carrierUp,
		m.checkTotal,
		m.recoveryTotal,
		m.recoveryFailureTot,
		m.softAttempts,
		m.lastCycleTimestamp,
		m.pingDurationSeconds,
		m.info,
		// Free Go runtime + process metrics — useful for "is the watchdog
		// itself healthy" alongside the network-side signals.
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return m
}

// gauge01 converts a bool to the 0/1 float64 Prometheus expects on a 0/1 gauge.
func gauge01(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
