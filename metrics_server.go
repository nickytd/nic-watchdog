// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsServer serves /metrics on addr backed by reg, returning a stop
// function that performs a bounded graceful shutdown. The listener bind
// happens before the goroutine returns, so a port-in-use error surfaces
// synchronously to the caller rather than at scrape time.
type metricsServer struct {
	srv  *http.Server
	addr string
	log  *slog.Logger
}

func newMetricsServer(addr string, reg prometheus.Gatherer, log *slog.Logger) *metricsServer {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		// Treat collector errors as 500 so scrape failures aren't silent.
		ErrorHandling: promhttp.HTTPErrorOnError,
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("nic-watchdog metrics: GET /metrics\n"))
	})

	return &metricsServer{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		addr: addr,
		log:  log,
	}
}

// run starts the server and blocks until ctx is canceled or ListenAndServe
// returns an error. Shutdown is bounded by a 3 s grace period.
func (s *metricsServer) run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("metrics server listening", slog.String("addr", s.addr))
		err := s.srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			s.log.Warn("metrics server shutdown returned error", slog.String("error", err.Error()))
		}
		return nil
	case err := <-errCh:
		return err
	}
}
