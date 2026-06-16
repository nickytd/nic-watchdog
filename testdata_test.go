// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

// Shared test fixtures. Centralized so dashboards built from real-data
// scenarios (e.g. testIfaceEth0 + testGatewayLAN) only need updating in one
// place, and so the goconst linter doesn't flag the same network-test
// literals in three places.
const (
	testIfaceEth0 = "eth0"
	testIfaceEnd0 = "end0"

	testGatewayLAN  = "10.0.0.1"
	testGatewayHome = "192.168.1.1"
	testGatewayAlt  = "192.168.0.1"

	// testTargetUnreachable is in TEST-NET-1 (RFC 5737) — guaranteed
	// unreachable, used as a deterministic ping-fail target.
	testTargetUnreachable = "192.0.2.1"
)
