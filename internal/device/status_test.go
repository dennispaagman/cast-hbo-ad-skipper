package device

import (
	"encoding/json"
	"testing"
)

// Shapes taken from real HBO Max receiver captures, with tokens and IDs replaced.
const (
	fullStatusNoAd = `{"type":"MEDIA_STATUS","status":[{"mediaSessionId":2,"playerState":"PLAYING","currentTime":1937.1,
		"media":{"contentId":"program-1","streamType":"LIVE","contentType":"application/dash+xml",
			"contentUrl":"https://example.invalid/live/manifest.mpd",
			"customData":{"programId":"program-1","restrictedToken":"token","mainStartingPosition":"LIVE_EDGE","adStrategy":"ad_free"},
			"metadata":{"metadataType":2,"title":"Europees Kampioenschap"},
			"tracks":[{"trackId":1,"type":"AUDIO"}],"breakClips":[],"breaks":[],"mediaCategory":"VIDEO"},
		"liveSeekableRange":{"start":1.9,"end":1935.4,"isMovingWindow":true,"isLiveDone":false}}],"requestId":4}`

	fullStatusInAd = `{"type":"MEDIA_STATUS","status":[{"mediaSessionId":2,"playerState":"PLAYING","currentTime":3044.6,
		"breakStatus":{"breakId":"break_1","breakClipId":"clip_2","currentBreakTime":5.0,"currentBreakClipTime":0.3},
		"media":{"contentId":"program-1","streamType":"LIVE","contentType":"application/dash+xml",
			"customData":{"programId":"program-1","restrictedToken":"token"},
			"metadata":{"title":"Europees Kampioenschap"},
			"breaks":[{"id":"break_1","breakClipIds":["clip_1","clip_2"],"duration":190.04,"isEmbedded":true}],
			"breakClips":[{"id":"clip_1","duration":5},{"id":"clip_2","duration":15}]}}]}`

	partialStatusInAd = `{"type":"MEDIA_STATUS","status":[{"mediaSessionId":2,"playerState":"BUFFERING","currentTime":3039.7,
		"breakStatus":{"breakId":"break_1","breakClipId":"clip_1"}}]}`

	emptyStatus = `{"type":"MEDIA_STATUS","status":[]}`
)

func TestParseMediaStatus(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantOK     bool
		wantBreak  bool
		wantMedia  bool
		wantState  string
		wantStream string
	}{
		{name: "full status without ad", payload: fullStatusNoAd, wantOK: true, wantMedia: true, wantState: "PLAYING", wantStream: "LIVE"},
		{name: "full status in ad", payload: fullStatusInAd, wantOK: true, wantBreak: true, wantMedia: true, wantState: "PLAYING", wantStream: "LIVE"},
		{name: "partial status in ad", payload: partialStatusInAd, wantOK: true, wantBreak: true, wantState: "BUFFERING"},
		{name: "no media session", payload: emptyStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, ok, err := parseMediaStatus([]byte(tt.payload))
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if st.InBreak != tt.wantBreak {
				t.Errorf("InBreak = %v, want %v", st.InBreak, tt.wantBreak)
			}
			if (st.Media != nil) != tt.wantMedia {
				t.Errorf("has media = %v, want %v", st.Media != nil, tt.wantMedia)
			}
			if st.PlayerState != tt.wantState {
				t.Errorf("PlayerState = %q, want %q", st.PlayerState, tt.wantState)
			}
			if st.StreamType != tt.wantStream {
				t.Errorf("StreamType = %q, want %q", st.StreamType, tt.wantStream)
			}
		})
	}
}

func TestParseReceiverStatus(t *testing.T) {
	payload := `{"type":"RECEIVER_STATUS","status":{"applications":[{"appId":"3A2AA214","displayName":"HBO Max",
		"isIdleScreen":false,"sessionId":"s1","transportId":"t1"}]}}`
	rs, _, err := parseReceiverStatus([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !rs.isHBOMax() || rs.TransportID != "t1" || rs.SessionID != "s1" {
		t.Errorf("unexpected receiver status: %+v", rs)
	}

	idle := `{"type":"RECEIVER_STATUS","status":{"applications":[{"appId":"E8C28D3C","displayName":"Backdrop","isIdleScreen":true}]}}`
	rs, _, err = parseReceiverStatus([]byte(idle))
	if err != nil {
		t.Fatal(err)
	}
	if rs != (receiverStatus{}) {
		t.Errorf("idle screen should yield empty status, got %+v", rs)
	}
}

func TestBuildLoadMedia(t *testing.T) {
	st, _, err := parseMediaStatus([]byte(fullStatusNoAd))
	if err != nil {
		t.Fatal(err)
	}
	out, err := buildLoadMedia(st.Media)
	if err != nil {
		t.Fatal(err)
	}

	var media map[string]any
	if err := json.Unmarshal(out, &media); err != nil {
		t.Fatal(err)
	}
	for _, k := range loadStripKeys {
		if _, ok := media[k]; ok {
			t.Errorf("%s should be stripped", k)
		}
	}
	for _, k := range []string{"contentId", "streamType", "contentType", "customData", "metadata"} {
		if _, ok := media[k]; !ok {
			t.Errorf("%s should be kept", k)
		}
	}
	cd := media["customData"].(map[string]any)
	if cd["restrictedToken"] != "token" {
		t.Errorf("customData not preserved: %v", cd)
	}
}
