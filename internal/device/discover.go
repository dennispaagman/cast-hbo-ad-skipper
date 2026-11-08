package device

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	castdns "github.com/vishen/go-chromecast/dns"

	"github.com/dennispaagman/cast-hbo-ad-skipper/internal/config"
)

// Discover emits cast devices until ctx is cancelled. Devices are re-emitted on every round so a
// device whose watcher exited (e.g. after a network outage) gets picked up again.
func Discover(ctx context.Context, conf *config.Config) <-chan castdns.CastEntry {
	ch := make(chan castdns.CastEntry)

	go func() {
		defer close(ch)

		if len(conf.Devices) != 0 {
			slog.Info("Connecting to configured devices...", "count", len(conf.Devices))
			emitStatic(ctx, conf.Devices, ch)
			return
		}

		slog.Info("Searching for devices...")
		for ctx.Err() == nil {
			if err := discoverRound(ctx, conf, ch); err != nil {
				slog.Error("Device discovery failed.", "error", err)
				sleepCtx(ctx, 10*time.Second)
			}
		}
	}()

	return ch
}

func emitStatic(ctx context.Context, devices []castdns.CastEntry, ch chan<- castdns.CastEntry) {
	for {
		for _, entry := range devices {
			if !send(ctx, ch, entry) {
				return
			}
		}
		if !sleepCtx(ctx, time.Minute) {
			return
		}
	}
}

func discoverRound(ctx context.Context, conf *config.Config, ch chan<- castdns.CastEntry) error {
	ctx, cancel := context.WithTimeout(ctx, conf.DiscoverInterval)
	defer cancel()

	entries, err := castdns.DiscoverCastDNSEntries(ctx, conf.NetworkInterface)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case entry, ok := <-entries:
			if !ok {
				<-ctx.Done()
				return nil
			}
			if !send(ctx, ch, entry) {
				return nil
			}
		}
	}
}

func send(ctx context.Context, ch chan<- castdns.CastEntry, entry castdns.CastEntry) bool {
	select {
	case ch <- entry:
		return true
	case <-ctx.Done():
		return false
	}
}

// supportsVideo reports whether the mDNS "ca" capability bitmask has the video-out bit set.
// Entries without capability info (e.g. statically configured devices) are assumed to support video.
func supportsVideo(entry castdns.CastEntry) bool {
	ca, ok := entry.InfoFields["ca"]
	if !ok {
		return true
	}
	v, err := strconv.Atoi(ca)
	if err != nil {
		return true
	}
	return v&1 != 0
}
