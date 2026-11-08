package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Device watchers beat after every successful status poll and before every reconnect attempt.
// A watcher whose last beat is older than the allowed age is stuck, which is what the container
// health check should catch so Docker restarts the service.
var (
	mu    sync.Mutex
	beats = map[string]time.Time{}
)

func Beat(key string) {
	mu.Lock()
	beats[key] = time.Now()
	mu.Unlock()
}

func Remove(key string) {
	mu.Lock()
	delete(beats, key)
	mu.Unlock()
}

func Check(maxAge time.Duration) error {
	mu.Lock()
	defer mu.Unlock()
	var stale []string
	for key, t := range beats {
		if age := time.Since(t); age > maxAge {
			stale = append(stale, fmt.Sprintf("%s (%s)", key, age.Round(time.Second)))
		}
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	return fmt.Errorf("stuck device watchers: %s", strings.Join(stale, ", "))
}

func Serve(ctx context.Context, addr string, maxAge time.Duration) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if err := Check(maxAge); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info("Health endpoint listening.", "addr", ln.Addr().String())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Probe queries the health endpoint; used as the container health check command. An empty
// address means the endpoint is disabled, which counts as healthy.
func Probe(addr string) error {
	if addr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s", resp.Status)
	}
	return nil
}
