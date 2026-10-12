package relay

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/minitap-ai/minitest-connector/internal/audit"
	"github.com/minitap-ai/minitest-connector/internal/selector"
	"github.com/minitap-ai/minitest-connector/internal/wire"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	// Connectors are not browsers: refuse any request carrying an Origin so a
	// web page can never open a session with a token it got hold of.
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

// PublicHandler serves the internet-facing listener: connector sessions only.
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/connect", s.handleConnect)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)
	if s.Draining() {
		http.Error(w, "relay draining", http.StatusServiceUnavailable)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		s.m.authFailures.Inc()
		s.audit.Event(ctx, audit.ConnectorAuthFailed, "remote_ip", ip, "reason", "missing_token", "relay_id", s.cfg.RelayID)
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	id, err := s.cfg.Auth.Authenticate(ctx, token)
	if err != nil {
		s.m.authFailures.Inc()
		reason := "invalid_token"
		status := http.StatusUnauthorized
		if !errors.Is(err, ErrUnauthorized) {
			reason, status = "auth_unavailable", http.StatusServiceUnavailable
		}
		s.audit.Event(ctx, audit.ConnectorAuthFailed, "remote_ip", ip, "token_prefix", TokenPrefix(token),
			"reason", reason, "relay_id", s.cfg.RelayID)
		http.Error(w, reason, status)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ys, err := yamux.Server(wire.NewWSConn(ws), wire.YamuxConfig())
	if err != nil {
		_ = ws.Close()
		return
	}
	defer func() { _ = ys.Close() }()

	ctrl, hello, err := s.handshake(ys)
	if err != nil {
		s.audit.Event(ctx, audit.InstanceRejected, "tenant_id", id.TenantID, "connector_id", id.ConnectorID,
			"remote_ip", ip, "reason", err.Error(), "relay_id", s.cfg.RelayID)
		return
	}
	sess := &session{
		id: randomID(), relayID: s.cfg.RelayID, identity: id, hello: hello, remoteIP: ip, ys: ys, ctrl: ctrl, since: time.Now(),
	}
	ack := wire.HelloAck{SessionID: sess.id, RelayID: s.cfg.RelayID, ConnectorID: id.ConnectorID}
	_ = ctrl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := wire.Write(ctrl, ack); err != nil {
		return
	}
	_ = ctrl.SetDeadline(time.Time{})

	s.register(sess)
	s.audit.Event(ctx, audit.InstanceConnected, sess.attrs()...)
	<-ys.CloseChan()
	s.unregister(sess)
	s.audit.Event(ctx, audit.InstanceDisconnected,
		append(sess.attrs(), "duration_s", int(time.Since(sess.since).Seconds()))...)
}

func (s *Server) handshake(ys *yamux.Session) (net.Conn, wire.Hello, error) {
	var hello wire.Hello
	type accepted struct {
		c   net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ys.Accept()
		ch <- accepted{c, err}
	}()
	var ctrl net.Conn
	select {
	case a := <-ch:
		if a.err != nil {
			return nil, hello, a.err
		}
		ctrl = a.c
	case <-time.After(15 * time.Second):
		return nil, hello, errors.New("no control stream")
	}
	_ = ctrl.SetDeadline(time.Now().Add(15 * time.Second))
	if err := wire.Read(ctrl, &hello); err != nil {
		return nil, hello, errors.New("bad hello")
	}
	if hello.ProtocolVersion != wire.ProtocolVersion {
		_ = wire.Write(ctrl, wire.HelloAck{Error: "unsupported protocol version, upgrade minitest-connector"})
		return nil, hello, errors.New("unsupported protocol version")
	}
	if _, err := selector.Parse(hello.Allow); err != nil {
		_ = wire.Write(ctrl, wire.HelloAck{Error: "invalid allow list: " + err.Error()})
		return nil, hello, errors.New("invalid allow list")
	}
	return ctrl, hello, nil
}

func (x *session) attrs() []any {
	return []any{
		"tenant_id", x.identity.TenantID, "connector_id", x.identity.ConnectorID,
		"session_id", x.id, "instance_id", x.hello.InstanceID, "hostname", x.hello.Hostname,
		"os", x.hello.OS, "arch", x.hello.Arch, "connector_version", x.hello.Version,
		"allow", x.hello.Allow, "ports", x.hello.Ports, "remote_ip", x.remoteIP, "relay_id", x.relayID,
	}
}
