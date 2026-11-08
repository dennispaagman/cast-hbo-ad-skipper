package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dennis/cast-hbo-ad-skipper/internal/config"
	"github.com/dennis/cast-hbo-ad-skipper/internal/device"
	"github.com/dennis/cast-hbo-ad-skipper/internal/health"
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

	if conf.HealthCheck {
		if err := health.Probe(conf.HealthAddr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: conf.LogLevel})))
	slog.Info("Starting cast-hbo-ad-skipper.", "dry_run", conf.DryRun, "live_only", conf.LiveOnly, "restart_cooldown", conf.RestartCooldown)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var (
		wg           sync.WaitGroup
		healthFailed atomic.Bool
	)
	if conf.HealthAddr != "" {
		wg.Go(func() {
			if err := health.Serve(ctx, conf.HealthAddr, device.HealthMaxAge(conf)); err != nil {
				slog.Error("Health endpoint failed.", "error", err)
				healthFailed.Store(true)
				cancel()
			}
		})
	}

	// Docker's restart policy only reacts to the process exiting, not to a failing health check,
	// so a stuck watcher has to take the process down itself.
	wg.Go(func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := health.Check(device.HealthMaxAge(conf)); err != nil {
					slog.Error("Unhealthy, exiting so the service gets restarted.", "error", err)
					os.Exit(1)
				}
			}
		}
	})

	for entry := range device.Discover(ctx, conf) {
		wg.Go(func() { device.Watch(ctx, conf, entry) })
	}

	slog.Info("Shutting down.")
	wg.Wait()
	if healthFailed.Load() {
		return 1
	}
	return 0
}
