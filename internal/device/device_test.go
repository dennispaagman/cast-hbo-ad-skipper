package device

import (
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	castdns "github.com/vishen/go-chromecast/dns"

	"github.com/dennis/cast-hbo-ad-skipper/internal/config"
)

type loadCall struct {
	transportID string
	media       json.RawMessage
}

func newTestDevice(t *testing.T, conf *config.Config) (*Device, *[]loadCall, *time.Time) {
	t.Helper()
	if conf == nil {
		conf = &config.Config{LiveOnly: true, RestartCooldown: 30 * time.Second}
	}
	now := time.Date(2026, 11, 8, 15, 41, 0, 0, time.UTC)
	var calls []loadCall
	d := &Device{
		conf:   conf,
		logger: slog.New(slog.DiscardHandler),
		now:    func() time.Time { return now },
		sendLoad: func(transportID string, media json.RawMessage) error {
			calls = append(calls, loadCall{transportID, media})
			return nil
		},
	}
	d.handleReceiver(receiverStatus{AppID: hboMaxAppID, DisplayName: hboMaxDisplayName, TransportID: "t1", SessionID: "s1"})
	return d, &calls, &now
}

func mustMedia(t *testing.T, payload string) mediaStatus {
	t.Helper()
	st, ok, err := parseMediaStatus([]byte(payload))
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	return st
}

func TestRestartsLiveStreamOnAd(t *testing.T) {
	d, calls, _ := newTestDevice(t, nil)

	d.handleMedia(mustMedia(t, fullStatusNoAd))
	if len(*calls) != 0 {
		t.Fatalf("restarted without an ad")
	}

	d.handleMedia(mustMedia(t, strings.Replace(partialStatusInAd, "BUFFERING", "PAUSED", 1)))
	if len(*calls) != 0 {
		t.Fatalf("restarted while paused")
	}

	d.handleMedia(mustMedia(t, fullStatusInAd))
	if len(*calls) != 1 {
		t.Fatalf("expected 1 restart, got %d", len(*calls))
	}
	call := (*calls)[0]
	if call.transportID != "t1" {
		t.Errorf("transport = %q", call.transportID)
	}
	if strings.Contains(string(call.media), "contentUrl") || !strings.Contains(string(call.media), `"contentId":"program-1"`) {
		t.Errorf("unexpected LOAD media: %s", call.media)
	}
}

func TestPartialStatusUsesLastKnownMedia(t *testing.T) {
	d, calls, _ := newTestDevice(t, nil)
	d.handleMedia(mustMedia(t, fullStatusNoAd))
	d.handleMedia(mustMedia(t, strings.Replace(partialStatusInAd, "BUFFERING", "PLAYING", 1)))
	if len(*calls) != 1 {
		t.Fatalf("expected restart from partial status, got %d", len(*calls))
	}
}

func TestRestartCooldown(t *testing.T) {
	d, calls, now := newTestDevice(t, nil)
	d.handleMedia(mustMedia(t, fullStatusInAd))
	d.handleMedia(mustMedia(t, fullStatusInAd))
	if len(*calls) != 1 {
		t.Fatalf("expected 1 restart within cooldown, got %d", len(*calls))
	}
	*now = now.Add(31 * time.Second)
	d.handleMedia(mustMedia(t, fullStatusInAd))
	if len(*calls) != 2 {
		t.Fatalf("expected restart after cooldown, got %d", len(*calls))
	}
}

func TestIgnoresNonLiveWhenLiveOnly(t *testing.T) {
	d, calls, _ := newTestDevice(t, nil)
	d.handleMedia(mustMedia(t, strings.Replace(fullStatusInAd, `"streamType":"LIVE"`, `"streamType":"BUFFERED"`, 1)))
	if len(*calls) != 0 {
		t.Fatalf("restarted a non-live stream")
	}

	d, calls, _ = newTestDevice(t, &config.Config{LiveOnly: false, RestartCooldown: time.Second})
	d.handleMedia(mustMedia(t, strings.Replace(fullStatusInAd, `"streamType":"LIVE"`, `"streamType":"BUFFERED"`, 1)))
	if len(*calls) != 1 {
		t.Fatalf("expected restart with live-only disabled")
	}
}

func TestIgnoresOtherApps(t *testing.T) {
	d, calls, _ := newTestDevice(t, nil)
	d.handleReceiver(receiverStatus{AppID: "CC38EB17", DisplayName: "NPOPlayer", TransportID: "t2", SessionID: "s2"})
	d.handleMedia(mustMedia(t, fullStatusInAd))
	if len(*calls) != 0 {
		t.Fatalf("restarted a non-HBO app")
	}
}

func TestDryRun(t *testing.T) {
	d, calls, _ := newTestDevice(t, &config.Config{LiveOnly: true, DryRun: true, RestartCooldown: time.Second})
	d.handleMedia(mustMedia(t, fullStatusInAd))
	if len(*calls) != 0 {
		t.Fatalf("dry run sent a LOAD")
	}
}

func TestAppChangeClearsMedia(t *testing.T) {
	d, calls, _ := newTestDevice(t, nil)
	d.handleMedia(mustMedia(t, fullStatusNoAd))
	d.handleReceiver(receiverStatus{AppID: hboMaxAppID, DisplayName: hboMaxDisplayName, TransportID: "t3", SessionID: "s3"})
	d.handleMedia(mustMedia(t, strings.Replace(partialStatusInAd, "BUFFERING", "PLAYING", 1)))
	if len(*calls) != 0 {
		t.Fatalf("restarted using media from a previous session")
	}
}

func TestMediaSessionChangeClearsMedia(t *testing.T) {
	for _, liveOnly := range []bool{true, false} {
		t.Run(strconv.FormatBool(liveOnly), func(t *testing.T) {
			d, calls, _ := newTestDevice(t, &config.Config{LiveOnly: liveOnly})
			d.handleMedia(mustMedia(t, fullStatusNoAd))
			partial := mustMedia(t, partialStatusInAd)
			partial.MediaSessionID++
			d.handleMedia(partial)
			if len(*calls) != 0 || d.media.Media != nil {
				t.Fatal("retained media from the previous media session")
			}

			payload := strings.ReplaceAll(fullStatusInAd, "program-1", "program-2")
			payload = strings.ReplaceAll(payload, `"streamType":"LIVE"`, `"streamType":"BUFFERED"`)
			full := mustMedia(t, payload)
			full.MediaSessionID = partial.MediaSessionID
			d.handleMedia(full)
			if liveOnly {
				if len(*calls) != 0 {
					t.Fatal("restarted a non-live stream after a media session change")
				}
			} else if len(*calls) != 1 || !strings.Contains(string((*calls)[0].media), `"contentId":"program-2"`) {
				t.Fatal("did not restart the new program when its details arrived")
			}
		})
	}
}

func TestDeviceAddress(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry castdns.CastEntry
		want  string
	}{
		{"IPv4", castdns.CastEntry{AddrV4: net.ParseIP("192.0.2.1"), Port: 8009}, "192.0.2.1:8009"},
		{"IPv6", castdns.CastEntry{AddrV6: net.ParseIP("2001:db8::1"), Port: 8009}, "[2001:db8::1]:8009"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceAddress(tc.entry); got != tc.want {
				t.Fatalf("address = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFailedRestartDoesNotStartCooldown(t *testing.T) {
	d, calls, _ := newTestDevice(t, nil)
	send := d.sendLoad
	d.sendLoad = func(string, json.RawMessage) error { return net.ErrClosed }
	d.handleMedia(mustMedia(t, fullStatusInAd))
	if !d.lastRestart.IsZero() {
		t.Fatal("failed restart started the cooldown")
	}

	d.sendLoad = send
	d.handleMedia(mustMedia(t, fullStatusInAd))
	if len(*calls) != 1 {
		t.Fatalf("expected retry right after a failed restart, got %d calls", len(*calls))
	}
}

func TestMissingMediaWarnsOncePerBreak(t *testing.T) {
	d, _, _ := newTestDevice(t, nil)
	var logs strings.Builder
	d.logger = slog.New(slog.NewTextHandler(&logs, nil))

	playing := strings.Replace(partialStatusInAd, "BUFFERING", "PLAYING", 1)
	for range 3 {
		d.handleMedia(mustMedia(t, playing))
	}
	if n := strings.Count(logs.String(), "stream details are not known yet"); n != 1 {
		t.Fatalf("expected 1 warning, got %d", n)
	}
}
