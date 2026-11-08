package device

import (
	"encoding/json"

	"github.com/vishen/go-chromecast/cast"
)

const (
	hboMaxAppID       = "3A2AA214"
	hboMaxDisplayName = "HBO Max"
)

// loadStripKeys are receiver-populated media fields that a sender never supplies in a LOAD.
// contentUrl is the receiver-resolved stream URL; dropping it makes the receiver request a fresh
// playback session from HBO, which is the session that comes back without the ad break.
var loadStripKeys = []string{"contentUrl", "tracks", "breaks", "breakClips", "mediaCategory"}

type receiverStatus struct {
	AppID       string
	DisplayName string
	TransportID string
	SessionID   string
}

func (r receiverStatus) isHBOMax() bool {
	return r.AppID == hboMaxAppID || r.DisplayName == hboMaxDisplayName
}

// parseReceiverStatus returns ok=false for messages that are not a RECEIVER_STATUS.
func parseReceiverStatus(payload []byte) (receiverStatus, bool, error) {
	var raw cast.ReceiverStatusResponse
	if err := json.Unmarshal(payload, &raw); err != nil {
		return receiverStatus{}, false, err
	}
	if raw.Type != "RECEIVER_STATUS" {
		return receiverStatus{}, false, nil
	}
	for _, app := range raw.Status.Applications {
		if !app.IsIdleScreen {
			return receiverStatus{AppID: app.AppId, DisplayName: app.DisplayName, TransportID: app.TransportId, SessionID: app.SessionId}, true, nil
		}
	}
	return receiverStatus{}, true, nil
}

type mediaStatus struct {
	MediaSessionID int
	PlayerState    string
	InBreak        bool
	BreakID        string
	BreakClipID    string

	// Set only when the status carries the full media description, which is what the sender
	// originally loaded and what we replay to restart the stream. Partial status updates omit it.
	Media      json.RawMessage
	ContentID  string
	StreamType string
	Title      string
}

func (m mediaStatus) isActive() bool {
	return m.PlayerState == "PLAYING" || m.PlayerState == "BUFFERING"
}

// parseMediaStatus returns ok=false for messages that are not a MEDIA_STATUS with a media session.
func parseMediaStatus(payload []byte) (mediaStatus, bool, error) {
	var raw struct {
		Type   string `json:"type"`
		Status []struct {
			MediaSessionID int    `json:"mediaSessionId"`
			PlayerState    string `json:"playerState"`
			BreakStatus    *struct {
				BreakID     string `json:"breakId"`
				BreakClipID string `json:"breakClipId"`
			} `json:"breakStatus"`
			Media json.RawMessage `json:"media"`
		} `json:"status"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return mediaStatus{}, false, err
	}
	if raw.Type != "MEDIA_STATUS" || len(raw.Status) == 0 {
		return mediaStatus{}, false, nil
	}

	s := raw.Status[0]
	st := mediaStatus{MediaSessionID: s.MediaSessionID, PlayerState: s.PlayerState}
	if s.BreakStatus != nil {
		st.InBreak = true
		st.BreakID = s.BreakStatus.BreakID
		st.BreakClipID = s.BreakStatus.BreakClipID
	}

	if len(s.Media) != 0 {
		var media struct {
			ContentID  string          `json:"contentId"`
			StreamType string          `json:"streamType"`
			CustomData json.RawMessage `json:"customData"`
			Metadata   struct {
				Title string `json:"title"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(s.Media, &media); err == nil && media.ContentID != "" && len(media.CustomData) != 0 {
			st.Media = s.Media
			st.ContentID = media.ContentID
			st.StreamType = media.StreamType
			st.Title = media.Metadata.Title
		}
	}

	return st, true, nil
}

// buildLoadMedia turns the media object from a MEDIA_STATUS back into the media object of a LOAD.
func buildLoadMedia(statusMedia json.RawMessage) (json.RawMessage, error) {
	var media map[string]json.RawMessage
	if err := json.Unmarshal(statusMedia, &media); err != nil {
		return nil, err
	}
	for _, k := range loadStripKeys {
		delete(media, k)
	}
	return json.Marshal(media)
}
