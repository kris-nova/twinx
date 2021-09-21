package obsws

// Event is broadcast by the server to each connected client when a recognized action occurs within OBS.
type Event interface {
	Type() string
	StreamTimecode() string
	RecTimecode() string
}

// https://github.com/Palakis/obs-websocket/blob/master/docs/generated/protocol.md#events
type _event struct {
	Type_           string `json:"update-type"`
	StreamTimecode_ string `json:"stream-timecode"`
	RecTimecode_    string `json:"rec-timecode"`
}

// Type returns the event's update type.
func (e _event) Type() string { return e.Type_ }

// StreamTimeode returns the event's stream timecode.
func (e _event) StreamTimecode() string { return e.StreamTimecode_ }

// RecTimecode returns the event's recording timecode.
func (e _event) RecTimecode() string { return e.RecTimecode_ }
