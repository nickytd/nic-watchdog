// Copyright 2025 nickytd
// SPDX-License-Identifier: Apache-2.0

// nic-watchdog monitors and recovers Ethernet connectivity on Raspberry Pi nodes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type Config struct {
	Iface         string
	PingTarget    string
	Gateway       string
	CheckInterval time.Duration
	Cooldown      time.Duration
	SoftMax       int
}

func (c Config) validate() error {
	if c.Iface == "" {
		return errors.New("iface must not be empty")
	}
	if c.PingTarget == "" {
		return errors.New("ping-target must not be empty")
	}
	if c.CheckInterval <= 0 {
		return fmt.Errorf("check-interval must be positive, got %v", c.CheckInterval)
	}
	if c.Cooldown <= 0 {
		return fmt.Errorf("cooldown must be positive, got %v", c.Cooldown)
	}
	if c.SoftMax <= 0 {
		return fmt.Errorf("soft-max must be positive, got %d", c.SoftMax)
	}
	return nil
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "nic-watchdog",
		Short: "NIC watchdog for Raspberry Pi Ethernet link recovery",
		RunE:  run,
	}

	flags := rootCmd.Flags()
	flags.String("iface", "eth0", "network interface")
	flags.String("ping-target", "8.8.8.8", "external connectivity check target")
	flags.String("gateway", "", "gateway IP (auto-detected if empty)")
	flags.Duration("check-interval", 10*time.Second, "check interval")
	flags.Duration("cooldown", 10*time.Minute, "minimum time between full interface cycles")
	flags.Int("soft-max", 3, "max soft recovery attempts before escalating")

	viper.SetEnvPrefix("NIC_WATCHDOG")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	_ = viper.BindPFlags(flags)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(_ *cobra.Command, _ []string) error {
	cfg := Config{
		Iface:         viper.GetString("iface"),
		PingTarget:    viper.GetString("ping-target"),
		Gateway:       viper.GetString("gateway"),
		CheckInterval: viper.GetDuration("check-interval"),
		Cooldown:      viper.GetDuration("cooldown"),
		SoftMax:       viper.GetInt("soft-max"),
	}

	if err := cfg.validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		cancel()
	}()

	w := NewWatchdog(cfg, logger)
	if err := w.Run(ctx); err != nil {
		logger.Error("watchdog failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}
