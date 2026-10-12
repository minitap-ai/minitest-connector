package connector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/minitap-ai/minitest-connector/internal/netguard"
	"github.com/minitap-ai/minitest-connector/internal/pipe"
	"github.com/minitap-ai/minitest-connector/internal/wire"
)

const (
	reasonGoAway   = "goaway"
	reasonClosed   = "closed"
	reasonCanceled = "canceled"
)

// AuthError is returned when the relay refuses the token.
type AuthError struct{ Status int }

func (e *AuthError) Error() string { return fmt.Sprintf("relay refused the token (HTTP %d)", e.Status) }

type session struct {
	ys        *yamux.Session
	ctrl      net.Conn
	sessionID string
	relayID   string
}

func (c *Connector) dial(ctx context.Context) (*session, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.cfg.Token)
	header.Set("User-Agent", "minitest-connector/"+c.cfg.Version)
	ws, resp, err := c.cfg.WSDialer.DialContext(ctx, c.cfg.RelayURL, header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return nil, &AuthError{Status: resp.StatusCode}
		}
		return nil, err
	}
	ys, err := yamux.Client(wire.NewWSConn(ws), wire.YamuxConfig())
	if err != nil {
		_ = ws.Close()
		return nil, err
	}
	ctrl, err := ys.OpenStream()
	if err != nil {
		_ = ys.Close()
		return nil, err
	}
	_ = ctrl.SetDeadline(time.Now().Add(15 * time.Second))
	hello := wire.Hello{
		ProtocolVersion: wire.ProtocolVersion,
		Version:         c.cfg.Version,
		InstanceID:      c.cfg.InstanceID,
		Hostname:        c.cfg.Hostname,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Allow:           c.allowList(),
		Ports:           c.cfg.Ports,
	}
	var ack wire.HelloAck
	err = wire.Write(ctrl, hello)
	if err == nil {
		err = wire.Read(ctrl, &ack)
	}
	if err != nil {
		_ = ys.Close()
		return nil, fmt.Errorf("handshake: %w", err)
	}
	if ack.Error != "" {
		_ = ys.Close()
		return nil, fmt.Errorf("relay rejected the instance: %s", ack.Error)
	}
	_ = ctrl.SetDeadline(time.Time{})
	return &session{ys: ys, ctrl: ctrl, sessionID: ack.SessionID, relayID: ack.RelayID}, nil
}

func (s *session) close() { _ = s.ys.Close() }

// serve handles streams until the session dies, ctx ends, or the relay sends
// GOAWAY. On GOAWAY it returns at once so a replacement session is opened, and
// keeps serving the old session's streams in the background until the relay
// closes it.
func (s *session) serve(ctx context.Context, c *Connector) string {
	goaway := make(chan struct{})
	go func() {
		for {
			var msg wire.Control
			if err := wire.Read(s.ctrl, &msg); err != nil {
				return
			}
			if msg.Type == wire.ControlGoAway {
				close(goaway)
				return
			}
		}
	}()
	go func() {
		for {
			st, err := s.ys.AcceptStream()
			if err != nil {
				return
			}
			go c.handleStream(ctx, st)
		}
	}()
	select {
	case <-ctx.Done():
		s.close()
		return reasonCanceled
	case <-s.ys.CloseChan():
		return reasonClosed
	case <-goaway:
		return reasonGoAway
	}
}

func (c *Connector) handleStream(ctx context.Context, st *yamux.Stream) {
	_ = st.SetDeadline(time.Now().Add(15 * time.Second))
	var open wire.StreamOpen
	if err := wire.Read(st, &open); err != nil {
		_ = st.Close()
		return
	}
	reply := func(r wire.StreamReply) bool {
		if err := wire.Write(st, r); err != nil || !r.OK {
			_ = st.Close()
			return false
		}
		return true
	}
	log := c.log.With("stream_id", open.StreamID, "host", open.Host, "port", open.Port)
	switch {
	case open.Host == "" || open.Port < 1 || open.Port > 65535:
		reply(wire.StreamReply{Code: wire.CodeBadRequest, Error: "invalid target"})
		return
	case !c.cfg.Allow.Matches(open.Host):
		log.Warn("connector.stream_denied", "reason", wire.CodeDeniedDomain)
		reply(wire.StreamReply{Code: wire.CodeDeniedDomain, Error: "domain not allowed by this connector"})
		return
	case !c.allowsPort(open.Port):
		log.Warn("connector.stream_denied", "reason", wire.CodeDeniedPort)
		reply(wire.StreamReply{Code: wire.CodeDeniedPort, Error: "port not allowed by this connector"})
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	target, err := c.cfg.Dial(dialCtx, "tcp", net.JoinHostPort(open.Host, strconv.Itoa(open.Port)))
	cancel()
	if err != nil {
		code := wire.CodeDialFailed
		var blocked *netguard.ErrBlocked
		if errors.As(err, &blocked) {
			code = wire.CodeBlockedAddress
		}
		log.Warn("connector.stream_denied", "reason", code, "error", err)
		reply(wire.StreamReply{Code: code, Error: err.Error()})
		return
	}
	if !reply(wire.StreamReply{OK: true}) {
		_ = target.Close()
		return
	}
	_ = st.SetDeadline(time.Time{})
	start := time.Now()
	up, down := pipe.Bidirectional(st, target)
	log.Info("connector.stream_closed", "bytes_up", up, "bytes_down", down,
		"duration_ms", time.Since(start).Milliseconds())
}
