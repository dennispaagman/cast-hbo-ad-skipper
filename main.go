package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/dennispaagman/cast-hbo-ad-skipper/internal/config"
	"github.com/dennispaagman/cast-hbo-ad-skipper/internal/device"
)

func main() {
	os.Exit(run())
}

func run() int {
	conf, err := config.Load(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		slog.Error("Invalid configuration.", "error", err)
		return 2
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: conf.LogLevel})))
	slog.Info("Starting cast-hbo-ad-skipper.", "dry_run", conf.DryRun, "live_only", conf.LiveOnly, "restart_cooldown", conf.RestartCooldown)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	for entry := range device.Discover(ctx, conf) {
		wg.Go(func() { device.Watch(ctx, conf, entry) })
	}

	slog.Info("Shutting down.")
	wg.Wait()
	return 0
}
