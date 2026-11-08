package device

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	castdns "github.com/vishen/go-chromecast/dns"

	"github.com/dennispaagman/cast-hbo-ad-skipper/internal/castconn"
	"github.com/dennispaagman/cast-hbo-ad-skipper/internal/config"
	"github.com/dennispaagman/cast-hbo-ad-skipper/internal/health"
)

var (
	active           sync.Map
	reconnectBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}
)

// HealthMaxAge is how long a watcher may go without a heartbeat before it counts as stuck: a
// poll, a reconnect attempt (dial plus the CONNECT and GET_STATUS writes, each bounded by the
// request timeout) and the longest reconnect backoff, plus slack.
func HealthMaxAge(c *config.Config) time.Duration {
	return c.PollInterval + 3*c.RequestTimeout + reconnectBackoff[len(reconnectBackoff)-1] + 30*time.Second
}

type Device struct {
	conf   *config.Config
	entry  castdns.CastEntry
	key    string
	logger *slog.Logger

	sendLoad func(transportID string, media json.RawMessage) error
	now      func() time.Time

	// State below is only touched from the Run goroutine.
	receiver         receiverStatus
	media            mediaStatus
	breakID          string
	lastRestart      time.Time
	restartSessionID int
	warnedNonLive    string
	warnedNoMedia    string
}

// Watch runs a watcher for entry until ctx is cancelled or the device stays unreachable.
// Calling it again for a device that is already being watched is a no-op.
func Watch(ctx context.Context, conf *config.Config, entry castdns.CastEntry) {
	if entry.Device == "Google Cast Group" || !supportsVideo(entry) {
		return
	}

	key := entry.UUID
	if key == "" {
		key = deviceAddress(entry)
	}
	if _, loaded := active.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	defer func() {
		health.Remove(key)
		active.Delete(key)
	}()

	name := entry.DeviceName
	if name == "" {
		name = entry.Device
	}
	d := &Device{conf: conf, entry: entry, key: key, logger: slog.With("device", name), now: time.Now}
	d.Run(ctx)
}

func (d *Device) Run(ctx context.Context) {
	failures := 0
	for {
		health.Beat(d.key)
		started := d.now()
		err := d.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if d.now().Sub(started) > 2*time.Minute {
			failures = 0
		}
		if failures >= len(reconnectBackoff) {
			d.logger.Error("Device unreachable, waiting for it to be rediscovered.", "error", err)
			return
		}
		wait := reconnectBackoff[failures]
		failures++
		d.logger.Warn("Lost connection to device, reconnecting.", "error", err, "in", wait)
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

func deviceAddress(entry castdns.CastEntry) string {
	return net.JoinHostPort(strings.Trim(entry.GetAddr(), "[]"), strconv.Itoa(entry.GetPort()))
}

func (d *Device) session(ctx context.Context) error {
	conn, err := castconn.Dial(ctx, deviceAddress(d.entry), d.conf.RequestTimeout, d.conf.NetworkInterface)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	d.sendLoad = func(transportID string, media json.RawMessage) error {
		return conn.Send(transportID, castconn.NamespaceMedia, map[string]any{"type": "LOAD", "media": media, "autoplay": true})
	}
	d.resetSession(receiverStatus{})

	if err := conn.Send(castconn.ReceiverID, castconn.NamespaceConnection, map[string]any{"type": "CONNECT"}); err != nil {
		return err
	}
	if err := conn.Send(castconn.ReceiverID, castconn.NamespaceReceiver, map[string]any{"type": "GET_STATUS"}); err != nil {
		return err
	}
	d.logger.Info("Connected to device.")

	// Ad starts are pushed by the receiver; polling only covers a missed push. The connection
	// itself is kept alive and checked by castconn's keepalive and read deadline.
	poll := time.NewTicker(d.conf.PollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.Done():
			return conn.Err()
		case msg := <-conn.Messages():
			if err := d.handleMessage(conn, msg); err != nil {
				return err
			}
		case <-poll.C:
			health.Beat(d.key)
			if d.receiver.isHBOMax() {
				if err := conn.Send(d.receiver.TransportID, castconn.NamespaceMedia, map[string]any{"type": "GET_STATUS"}); err != nil {
					return err
				}
			}
		}
	}
}

func (d *Device) handleMessage(conn *castconn.Conn, msg castconn.Message) error {
	switch msg.Namespace {
	case castconn.NamespaceReceiver:
		rs, ok, err := parseReceiverStatus(msg.Payload)
		if err != nil || !ok {
			return nil //nolint:nilerr // Unparseable or unrelated messages are ignored.
		}
		newSession := rs.SessionID != d.receiver.SessionID
		d.handleReceiver(rs)
		if newSession && rs.isHBOMax() {
			if err := conn.Send(rs.TransportID, castconn.NamespaceConnection, map[string]any{"type": "CONNECT"}); err != nil {
				return err
			}
			return conn.Send(rs.TransportID, castconn.NamespaceMedia, map[string]any{"type": "GET_STATUS"})
		}
	case castconn.NamespaceMedia:
		if !d.receiver.isHBOMax() {
			return nil
		}
		ms, ok, err := parseMediaStatus(msg.Payload)
		if err != nil || !ok {
			return nil //nolint:nilerr // Unparseable or unrelated messages are ignored.
		}
		d.handleMedia(ms)
	}
	return nil
}

func (d *Device) resetSession(rs receiverStatus) {
	d.receiver = rs
	d.media = mediaStatus{}
	d.breakID = ""
	d.restartSessionID = 0
}

func (d *Device) handleReceiver(rs receiverStatus) {
	if rs.SessionID == d.receiver.SessionID {
		d.receiver = rs
		return
	}
	switch {
	case rs.isHBOMax():
		d.logger.Info("HBO Max is casting.")
	case d.receiver.isHBOMax():
		d.logger.Info("HBO Max stopped casting.")
	}
	d.resetSession(rs)
}

func (d *Device) handleMedia(st mediaStatus) {
	if !d.receiver.isHBOMax() {
		return
	}

	if st.MediaSessionID != d.media.MediaSessionID {
		d.media = mediaStatus{MediaSessionID: st.MediaSessionID}
		d.breakID = ""
		d.warnedNonLive = ""
	}

	if st.Media != nil {
		if st.ContentID != d.media.ContentID {
			d.logger.Info("Watching stream.", "title", st.Title, "stream_type", st.StreamType, "content_id", st.ContentID)
		}
		d.media = st
	}

	if d.restartSessionID != 0 && st.MediaSessionID > d.restartSessionID && st.PlayerState == "PLAYING" {
		took := d.now().Sub(d.lastRestart).Round(100 * time.Millisecond)
		if st.InBreak {
			d.logger.Warn("Stream restarted but an ad is still playing.", "took", took)
		} else {
			d.logger.Info("Stream restarted without the ad.", "took", took)
		}
		d.restartSessionID = 0
	}

	if !st.InBreak {
		if d.breakID != "" {
			d.logger.Info("Ad break ended.", "break_id", d.breakID)
			d.breakID = ""
		}
		return
	}

	if st.BreakID != d.breakID {
		d.logger.Info("Ad break started.", "break_id", st.BreakID, "clip_id", st.BreakClipID)
		d.breakID = st.BreakID
	}

	if !st.isActive() {
		return
	}
	if d.media.Media == nil {
		if d.warnedNoMedia != st.BreakID {
			d.logger.Warn("Ad detected but the stream details are not known yet; waiting for a full status update.")
			d.warnedNoMedia = st.BreakID
		}
		return
	}
	if d.conf.LiveOnly && d.media.StreamType != "LIVE" {
		if d.warnedNonLive != d.media.ContentID {
			d.logger.Info("Ignoring ad on a non-live stream.", "stream_type", d.media.StreamType)
			d.warnedNonLive = d.media.ContentID
		}
		return
	}
	if !d.lastRestart.IsZero() && d.now().Sub(d.lastRestart) < d.conf.RestartCooldown {
		d.logger.Debug("Ad detected during restart cooldown.", "since_restart", d.now().Sub(d.lastRestart))
		return
	}

	d.restart(st)
}

func (d *Device) restart(st mediaStatus) {
	if d.conf.DryRun {
		d.lastRestart = d.now()
		d.logger.Info("Dry run: would restart the stream to skip the ad.", "break_id", st.BreakID)
		return
	}

	media, err := buildLoadMedia(d.media.Media)
	if err != nil {
		d.logger.Error("Failed to build restart request.", "error", err)
		return
	}

	d.logger.Info("Restarting stream to skip the ad.", "break_id", st.BreakID, "title", d.media.Title)
	if err := d.sendLoad(d.receiver.TransportID, media); err != nil {
		d.logger.Error("Failed to restart stream.", "error", err)
		return
	}
	d.lastRestart = d.now()
	d.restartSessionID = st.MediaSessionID
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
