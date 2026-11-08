package health

import (
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	t.Cleanup(func() { Remove("a"); Remove("b") })

	if err := Check(time.Minute); err != nil {
		t.Fatalf("no watchers should be healthy: %v", err)
	}

	Beat("a")
	if err := Check(time.Minute); err != nil {
		t.Fatalf("fresh beat should be healthy: %v", err)
	}

	mu.Lock()
	beats["b"] = time.Now().Add(-2 * time.Minute)
	mu.Unlock()
	if err := Check(time.Minute); err == nil {
		t.Fatal("stale beat should be unhealthy")
	}

	Remove("b")
	if err := Check(time.Minute); err != nil {
		t.Fatalf("removed watcher should not count: %v", err)
	}
}

func TestProbeDisabledEndpointIsHealthy(t *testing.T) {
	if err := Probe(""); err != nil {
		t.Fatalf("disabled endpoint should be healthy: %v", err)
	}
}
