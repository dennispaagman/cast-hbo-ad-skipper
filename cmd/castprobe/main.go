package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/vishen/go-chromecast/application"
	api "github.com/vishen/go-chromecast/cast/proto"
)

func main() {
	addr := flag.String("addr", "", "Chromecast IP address")
	port := flag.Int("port", 8009, "Chromecast port")
	interval := flag.Duration("interval", time.Second, "status poll interval")
	once := flag.Bool("once", false, "print one status round and exit")
	fullPath := flag.String("full", "", "write full JSON dumps (on structural change) to this file")
	flag.Parse()
	if *addr == "" {
		log.Fatal("-addr is required")
	}

	full := os.Stdout
	if *fullPath != "" {
		f, err := os.OpenFile(*fullPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		full = f
	}

	lastShape := map[string]string{}
	var lastMsg atomic.Int64
	lastMsg.Store(time.Now().UnixNano())
	go func() {
		for range time.Tick(time.Second) {
			if time.Since(time.Unix(0, lastMsg.Load())) > 15*time.Second {
				log.Fatal("no messages for 15s, exiting so the wrapper can reconnect")
			}
		}
	}()
	app := application.NewApplication(application.WithCacheDisabled(true))
	app.AddMessageFunc(func(msg *api.CastMessage) {
		lastMsg.Store(time.Now().UnixNano())
		payload := []byte(msg.GetPayloadUtf8())
		if len(payload) == 0 || bytes.Contains(payload, []byte(`"PONG"`)) || bytes.Contains(payload, []byte(`"PING"`)) {
			return
		}
		now := time.Now().Format("15:04:05.000")
		ns := msg.GetNamespace()

		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			fmt.Printf("%s RAW ns=%s %s\n", now, ns, payload)
			return
		}
		typ, _ := m["type"].(string)

		if typ == "MEDIA_STATUS" {
			printTimeline(now, m)
		} else if ns != "urn:x-cast:com.google.cast.receiver" && ns != "urn:x-cast:com.google.cast.tp.connection" {
			fmt.Printf("%s MSG ns=%s %s\n", now, ns, payload)
		}

		key := ns + "|" + typ
		shape := shapeOf(m)
		if shape == lastShape[key] {
			return
		}
		lastShape[key] = shape
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, payload, "", "  ")
		fmt.Fprintf(full, "=== %s ns=%s type=%s\n%s\n", now, ns, typ, pretty.String())
		if full != os.Stdout {
			fmt.Printf("%s CHANGED ns=%s type=%s (full dump written)\n", now, ns, typ)
		}
	})

	if err := app.Start(*addr, *port); err != nil {
		log.Fatalf("start: %v", err)
	}
	for {
		if err := app.Update(); err != nil {
			log.Fatalf("update: %v", err)
		}
		if *once {
			time.Sleep(500 * time.Millisecond)
			return
		}
		time.Sleep(*interval)
	}
}

func printTimeline(now string, m map[string]any) {
	statuses, _ := m["status"].([]any)
	if len(statuses) == 0 {
		fmt.Printf("%s MEDIA no-status\n", now)
		return
	}
	s, _ := statuses[0].(map[string]any)
	media, _ := s["media"].(map[string]any)
	meta, _ := media["metadata"].(map[string]any)
	live, _ := s["liveSeekableRange"].(map[string]any)
	cur, _ := s["currentTime"].(float64)
	liveEnd, _ := live["end"].(float64)
	breakStatus, _ := json.Marshal(s["breakStatus"])
	extended, _ := json.Marshal(s["extendedStatus"])
	fmt.Printf("%s MEDIA state=%v idle=%v t=%.1f liveEnd=%.1f behind=%.1f title=%q contentId=%v breaks=%d breakStatus=%s ext=%s\n",
		now, s["playerState"], s["idleReason"], cur, liveEnd, liveEnd-cur,
		meta["title"], media["contentId"], lenOf(media["breaks"]), breakStatus, extended)
}

func lenOf(v any) int {
	l, _ := v.([]any)
	return len(l)
}

// shapeOf strips fields that change every poll so only structural changes trigger a full dump.
func shapeOf(m map[string]any) string {
	var walk func(o any) any
	walk = func(o any) any {
		switch v := o.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, val := range v {
				switch k {
				case "requestId", "currentTime", "liveSeekableRange", "currentBreakTime", "currentBreakClipTime":
					continue
				}
				out[k] = walk(val)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i := range v {
				out[i] = walk(v[i])
			}
			return out
		}
		return o
	}
	b, _ := json.Marshal(walk(m))
	return string(b)
}
