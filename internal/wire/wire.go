// Package wire defines the connector ↔ relay protocol.
//
// A connector instance opens a WebSocket to the relay, authenticated with its
// connector token, and runs a yamux session over it (connector = yamux client).
// The connector opens the first stream as the control stream: it sends Hello,
// the relay answers HelloAck, then the relay may push Control messages
// (GOAWAY on drain). Every other stream is opened by the relay for one
// device connection: it writes StreamOpen, the connector answers StreamReply,
// and on success both sides pipe raw bytes.
//
// Messages are framed as a 2-byte big-endian length followed by JSON, so no
// buffering ever swallows the raw bytes that follow a header.
package wire

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// ProtocolVersion is bumped on incompatible changes.
const ProtocolVersion = 1

// MaxMessageSize bounds a framed message.
const MaxMessageSize = 16 << 10

// Hello is sent by the connector on the control stream.
type Hello struct {
	ProtocolVersion int      `json:"protocol_version"`
	Version         string   `json:"version"`
	InstanceID      string   `json:"instance_id"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Allow           []string `json:"allow"`
	Ports           []int    `json:"ports"`
}

// HelloAck is the relay's answer to Hello.
type HelloAck struct {
	SessionID   string `json:"session_id"`
	RelayID     string `json:"relay_id"`
	ConnectorID string `json:"connector_id"`
	Error       string `json:"error,omitempty"`
}

// Control is pushed by the relay on the control stream.
type Control struct {
	Type string `json:"type"`
}

// ControlGoAway asks the connector to open a replacement session and stop
// using this one once its streams end.
const ControlGoAway = "goaway"

// StreamOpen is written by the relay at the start of a data stream.
type StreamOpen struct {
	StreamID        string `json:"stream_id"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	EgressSessionID string `json:"egress_session_id,omitempty"`
}

// StreamReply is the connector's answer to StreamOpen.
type StreamReply struct {
	OK    bool   `json:"ok"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// Reply codes.
const (
	CodeDeniedDomain   = "denied_domain"
	CodeDeniedPort     = "denied_port"
	CodeBlockedAddress = "blocked_address"
	CodeDialFailed     = "dial_failed"
	CodeBadRequest     = "bad_request"
)

// Write frames v onto w.
func Write(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > MaxMessageSize {
		return fmt.Errorf("wire: message too large (%d bytes)", len(body))
	}
	frame := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(frame, uint16(len(body))) //nolint:gosec // bounded by MaxMessageSize above
	copy(frame[2:], body)
	_, err = w.Write(frame)
	return err
}

// Read reads one framed message from r into v.
func Read(r io.Reader, v any) error {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint16(size[:]))
	if n > MaxMessageSize {
		return fmt.Errorf("wire: message too large (%d bytes)", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}
