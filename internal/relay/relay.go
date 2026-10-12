// Package relay is the Minitap-side half of the tunnel. It holds connector
// sessions (public WSS listener) and serves HTTP CONNECT requests from device
// egress agents (cluster-internal listener), opening one yamux stream into the
// right connector per request. Pods are interchangeable: a CONNECT landing on a
// pod without a session for its connector is forwarded to the peer that has one.
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/minitap-ai/minitest-connector/internal/audit"
	"github.com/minitap-ai/minitest-connector/internal/runtoken"
	"github.com/minitap-ai/minitest-connector/internal/wire"
)

// Config configures a relay pod.
type Config struct {
	RelayID           string // pod name, reported to connectors
	Auth              Authenticator
	Tokens            *runtoken.Verifier
	Peers             PeerFinder // nil disables cross-pod forwarding
	TrustProxyHeaders bool       // read the client IP from X-Real-IP (behind ingress-nginx)
	Logger            *slog.Logger
	Registerer        prometheus.Registerer
}

// Server is a relay pod.
type Server struct {
	cfg   Config
	log   *slog.Logger
	audit *audit.Logger
	m     *metrics

	mu       sync.Mutex
	sessions map[string][]*session // connector id -> sessions
	draining atomic.Bool
	inflight atomic.Int64
}

type session struct {
	id        string
	relayID   string
	identity  Identity
	hello     wire.Hello
	remoteIP  string
	ys        *yamux.Session
	ctrl      net.Conn
	ctrlMu    sync.Mutex
	since     time.Time
	active    atomic.Int64
	goingAway atomic.Bool
}

// New returns a relay server.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RelayID == "" {
		cfg.RelayID = "relay-" + randomID()
	}
	return &Server{
		cfg:      cfg,
		log:      cfg.Logger,
		audit:    audit.New(cfg.Logger),
		m:        newMetrics(cfg.Registerer),
		sessions: map[string][]*session{},
	}
}

func (s *Server) register(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.identity.ConnectorID] = append(s.sessions[sess.identity.ConnectorID], sess)
	s.m.sessions.Inc()
}

func (s *Server) unregister(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.sessions[sess.identity.ConnectorID]
	for i, x := range list {
		if x == sess {
			list = append(list[:i], list[i+1:]...)
			s.m.sessions.Dec()
			break
		}
	}
	if len(list) == 0 {
		delete(s.sessions, sess.identity.ConnectorID)
	} else {
		s.sessions[sess.identity.ConnectorID] = list
	}
}

// pick returns the least loaded usable session of a connector owned by tenant.
func (s *Server) pick(tenantID, connectorID string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *session
	for _, x := range s.sessions[connectorID] {
		if x.identity.TenantID != tenantID || x.goingAway.Load() || x.ys.IsClosed() {
			continue
		}
		if best == nil || x.active.Load() < best.active.Load() {
			best = x
		}
	}
	return best
}

// HasConnector reports whether this pod holds a usable session for connectorID.
func (s *Server) HasConnector(connectorID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.sessions[connectorID] {
		if !x.goingAway.Load() && !x.ys.IsClosed() {
			return true
		}
	}
	return false
}

// DropConnector closes every session of a connector, e.g. on revocation.
func (s *Server) DropConnector(ctx context.Context, connectorID, reason string) int {
	s.mu.Lock()
	list := append([]*session(nil), s.sessions[connectorID]...)
	s.mu.Unlock()
	for _, x := range list {
		_ = x.ys.Close()
	}
	if len(list) > 0 {
		s.audit.Event(ctx, audit.ConnectorSessionsDrop, "connector_id", connectorID,
			"tenant_id", list[0].identity.TenantID, "sessions", len(list), "reason", reason, "relay_id", s.cfg.RelayID)
	}
	return len(list)
}

// Drain stops accepting connector sessions, asks connected instances to move
// to another pod, and waits for in-flight streams to finish or ctx to end.
func (s *Server) Drain(ctx context.Context) {
	s.draining.Store(true)
	s.mu.Lock()
	var all []*session
	for _, list := range s.sessions {
		all = append(all, list...)
	}
	s.mu.Unlock()
	for _, x := range all {
		x.sendGoAway()
	}
	s.waitInflight(ctx)
	for _, x := range all {
		_ = x.ys.Close()
	}
}

func (s *Server) waitInflight(ctx context.Context) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for s.inflight.Load() > 0 {
		select {
		case <-ctx.Done():
			s.log.Warn("relay.drain_timeout", "inflight", s.inflight.Load())
			return
		case <-tick.C:
		}
	}
}

// Draining reports whether Drain was called.
func (s *Server) Draining() bool { return s.draining.Load() }

func (x *session) sendGoAway() {
	x.goingAway.Store(true)
	x.ctrlMu.Lock()
	defer x.ctrlMu.Unlock()
	_ = x.ctrl.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = wire.Write(x.ctrl, wire.Control{Type: wire.ControlGoAway})
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
