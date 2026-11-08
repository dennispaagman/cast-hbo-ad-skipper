package config

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	castdns "github.com/vishen/go-chromecast/dns"
)

const envPrefix = "HBO_AD_SKIPPER_"

type Config struct {
	Devices          []castdns.CastEntry
	NetworkInterface *net.Interface

	DiscoverInterval time.Duration
	PollInterval     time.Duration
	RequestTimeout   time.Duration
	RestartCooldown  time.Duration
	LiveOnly         bool
	DryRun           bool
	HealthAddr       string
	HealthCheck      bool

	LogLevel slog.Level
}

func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("cast-hbo-ad-skipper", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: cast-hbo-ad-skipper [flags]\n\n"+
			"Restarts HBO Max live streams on Chromecast devices when an ad break starts.\n"+
			"Every flag can also be set with an environment variable, e.g. -restart-cooldown -> %sRESTART_COOLDOWN.\n\n", envPrefix)
		fs.PrintDefaults()
	}

	devices := fs.String("devices", env("devices", ""), "Comma-separated device addresses (host or host:port). Disables mDNS discovery.")
	iface := fs.String("network-interface", env("network-interface", ""), "Network interface to use for discovery and connections")
	discoverInterval := fs.Duration("discover-interval", envDuration("discover-interval", 5*time.Minute), "Interval to restart device discovery")
	pollInterval := fs.Duration("poll-interval", envDuration("poll-interval", 2*time.Second), "Media status poll interval while HBO Max is casting; a fallback, as the device pushes ad starts")
	requestTimeout := fs.Duration("request-timeout", envDuration("request-timeout", 10*time.Second), "Timeout for connecting, writes and silence on the connection before reconnecting")
	restartCooldown := fs.Duration("restart-cooldown", envDuration("restart-cooldown", 30*time.Second), "Minimum time between two restarts on the same device")
	liveOnly := fs.Bool("live-only", envBool("live-only", true), "Only restart live streams (restarting on-demand content would lose the playback position)")
	dryRun := fs.Bool("dry-run", envBool("dry-run", false), "Log detected ads without restarting the stream")
	healthAddr := fs.String("health-addr", env("health-addr", "127.0.0.1:8932"), "Address for the /healthz endpoint (empty disables it)")
	healthCheck := fs.Bool("healthcheck", false, "Query the health endpoint at -health-addr and exit non-zero when unhealthy")
	logLevel := fs.String("log-level", env("log-level", "info"), "Log level (debug, info, warn, error)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	c := &Config{
		DiscoverInterval: *discoverInterval,
		PollInterval:     *pollInterval,
		RequestTimeout:   *requestTimeout,
		RestartCooldown:  *restartCooldown,
		LiveOnly:         *liveOnly,
		DryRun:           *dryRun,
		HealthAddr:       *healthAddr,
		HealthCheck:      *healthCheck,
	}

	if c.HealthCheck {
		return c, nil
	}

	if err := c.LogLevel.UnmarshalText([]byte(*logLevel)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", *logLevel, err)
	}

	if *iface != "" {
		i, err := net.InterfaceByName(*iface)
		if err != nil {
			return nil, fmt.Errorf("network interface %q: %w", *iface, err)
		}
		c.NetworkInterface = i
	}

	if *devices != "" {
		for _, addr := range strings.Split(*devices, ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			entry, err := parseDevice(addr)
			if err != nil {
				return nil, err
			}
			c.Devices = append(c.Devices, entry)
		}
		if len(c.Devices) == 0 {
			return nil, fmt.Errorf("-devices %q contains no device addresses", *devices)
		}
	}

	if c.PollInterval <= 0 || c.RequestTimeout <= 0 || c.DiscoverInterval <= 0 {
		return nil, errors.New("intervals and timeouts must be positive")
	}

	return c, nil
}

func parseDevice(addr string) (castdns.CastEntry, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		host, portStr = strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]"), "8009"
	}
	if host == "" {
		return castdns.CastEntry{}, fmt.Errorf("device %q: missing host", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return castdns.CastEntry{}, fmt.Errorf("device %q: invalid port", addr)
	}
	ip, err := net.ResolveIPAddr("ip", host)
	if err != nil {
		return castdns.CastEntry{}, fmt.Errorf("device %q: %w", addr, err)
	}
	entry := castdns.CastEntry{Port: port, UUID: addr, DeviceName: addr}
	if ip4 := ip.IP.To4(); ip4 != nil {
		entry.AddrV4 = ip4
	} else {
		entry.AddrV6 = ip.IP
	}
	return entry, nil
}

func envKey(name string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func env(name, def string) string {
	if v, ok := os.LookupEnv(envKey(name)); ok {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	return envParse(name, def, time.ParseDuration)
}

func envBool(name string, def bool) bool {
	return envParse(name, def, strconv.ParseBool)
}

func envParse[T any](name string, def T, parse func(string) (T, error)) T {
	v, ok := os.LookupEnv(envKey(name))
	if !ok {
		return def
	}
	parsed, err := parse(v)
	if err != nil {
		slog.Warn("Ignoring invalid environment variable.", "env", envKey(name), "value", v, "error", err)
		return def
	}
	return parsed
}
