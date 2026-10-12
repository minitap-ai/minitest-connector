package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/minitap-ai/minitest-connector/internal/audit"
	"github.com/minitap-ai/minitest-connector/internal/pipe"
	"github.com/minitap-ai/minitest-connector/internal/runtoken"
	"github.com/minitap-ai/minitest-connector/internal/wire"
)

// HopHeader marks a CONNECT forwarded by a peer relay pod, so the receiving
// pod serves it locally and never forwards again.
const HopHeader = "X-Minitap-Relay-Hop"

// InternalHandler serves the cluster-internal listener: CONNECT from device
// egress agents and peers, peer lookups, probes and metrics. NetworkPolicy
// restricts who reaches it; every CONNECT is still authorized by its run token.
func (s *Server) InternalHandler(gatherer prometheus.Gatherer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/connectors/{id}", func(w http.ResponseWriter, r *http.Request) {
		if s.HasConnector(r.PathValue("id")) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.Draining() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	if gatherer != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			s.handleCONNECT(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type denial struct {
	status int
	reason string
}

func (s *Server) handleCONNECT(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	s.inflight.Add(1)
	defer s.inflight.Add(-1)

	token, _ := strings.CutPrefix(r.Header.Get("Proxy-Authorization"), "Bearer ")
	scope, err := s.cfg.Tokens.Verify(token)
	if err != nil {
		s.deny(ctx, w, nil, r.Host, denial{http.StatusProxyAuthRequired, "invalid_run_token"})
		return
	}
	host, port, err := splitTarget(r.Host)
	if err != nil {
		s.deny(ctx, w, scope, r.Host, denial{http.StatusBadRequest, "invalid_target"})
		return
	}
	if !scope.Selectors.Matches(host) {
		s.deny(ctx, w, scope, r.Host, denial{http.StatusForbidden, "domain_not_in_app_config"})
		return
	}
	if !scope.AllowsPort(port) {
		s.deny(ctx, w, scope, r.Host, denial{http.StatusForbidden, "port_not_in_app_config"})
		return
	}

	sess := s.pick(scope.TenantID, scope.ConnectorID)
	if sess == nil {
		if r.Header.Get(HopHeader) == "" && s.cfg.Peers != nil {
			if peer, ok := s.cfg.Peers.Find(ctx, scope.ConnectorID); ok {
				s.forwardToPeer(ctx, w, r, peer, scope)
				return
			}
		}
		s.deny(ctx, w, scope, r.Host, denial{http.StatusServiceUnavailable, "connector_offline"})
		return
	}

	st, err := sess.ys.OpenStream()
	if err != nil {
		s.deny(ctx, w, scope, r.Host, denial{http.StatusServiceUnavailable, "connector_session_lost"})
		return
	}
	streamID := randomID()
	_ = st.SetDeadline(time.Now().Add(20 * time.Second))
	var reply wire.StreamReply
	err = wire.Write(st, wire.StreamOpen{StreamID: streamID, Host: host, Port: port, EgressSessionID: scope.EgressSessionID})
	if err == nil {
		err = wire.Read(st, &reply)
	}
	if err != nil {
		_ = st.Close()
		s.deny(ctx, w, scope, r.Host, denial{http.StatusBadGateway, "connector_no_reply"})
		return
	}
	if !reply.OK {
		_ = st.Close()
		s.deny(ctx, w, scope, r.Host, denialForCode(reply.Code))
		return
	}
	_ = st.SetDeadline(time.Time{})

	client, buffered, err := hijack(w)
	if err != nil {
		_ = st.Close()
		return
	}
	if buffered != nil {
		if _, err := io.Copy(st, buffered); err != nil {
			_ = st.Close()
			_ = client.Close()
			return
		}
	}

	base := append(scopeAttrs(scope), "stream_id", streamID, "session_id", sess.id, "target", r.Host, "relay_id", s.cfg.RelayID)
	s.audit.Event(ctx, audit.StreamOpened, base...)
	s.m.streams.WithLabelValues("opened").Inc()
	s.m.activeStreams.Inc()
	sess.active.Add(1)
	up, down := pipe.Bidirectional(client, st)
	sess.active.Add(-1)
	s.m.activeStreams.Dec()
	s.m.bytes.WithLabelValues("up").Add(float64(up))
	s.m.bytes.WithLabelValues("down").Add(float64(down))
	s.audit.Event(ctx, audit.StreamClosed, append(base, "bytes_up", up, "bytes_down", down,
		"duration_ms", time.Since(start).Milliseconds())...)
}

func (s *Server) deny(ctx context.Context, w http.ResponseWriter, scope *runtoken.Scope, target string, d denial) {
	s.m.streams.WithLabelValues("denied_" + d.reason).Inc()
	s.audit.Event(ctx, audit.StreamDenied, append(scopeAttrs(scope), "target", target, "reason", d.reason,
		"status", d.status, "relay_id", s.cfg.RelayID)...)
	http.Error(w, d.reason, d.status)
}

func denialForCode(code string) denial {
	switch code {
	case wire.CodeDeniedDomain:
		return denial{http.StatusForbidden, "domain_not_allowed_by_connector"}
	case wire.CodeDeniedPort:
		return denial{http.StatusForbidden, "port_not_allowed_by_connector"}
	case wire.CodeBlockedAddress:
		return denial{http.StatusForbidden, "address_blocked_by_connector"}
	case wire.CodeDialFailed:
		return denial{http.StatusBadGateway, "target_unreachable"}
	default:
		return denial{http.StatusBadGateway, "connector_error"}
	}
}

func scopeAttrs(scope *runtoken.Scope) []any {
	if scope == nil {
		return nil
	}
	return []any{"tenant_id", scope.TenantID, "app_id", scope.AppID, "connector_id", scope.ConnectorID,
		"egress_session_id", scope.EgressSessionID}
}

func splitTarget(hostport string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || host == "" {
		return "", 0, fmt.Errorf("invalid target %q", hostport)
	}
	return host, port, nil
}

// hijack answers 200 and takes over the client connection. It returns any
// bytes the client already sent after the CONNECT header (often the TLS
// ClientHello) so they are not lost.
func hijack(w http.ResponseWriter) (net.Conn, io.Reader, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack unsupported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if n := rw.Reader.Buffered(); n > 0 {
		b, _ := rw.Peek(n)
		return conn, strings.NewReader(string(b)), nil
	}
	return conn, nil, nil
}

// forwardToPeer replays the CONNECT on the pod holding the connector and
// splices the two connections. The peer re-verifies the run token.
func (s *Server) forwardToPeer(ctx context.Context, w http.ResponseWriter, r *http.Request, peer string, scope *runtoken.Scope) {
	d := net.Dialer{Timeout: 5 * time.Second}
	pc, err := d.DialContext(ctx, "tcp", peer)
	if err != nil {
		s.cfg.Peers.Forget(scope.ConnectorID)
		s.deny(ctx, w, scope, r.Host, denial{http.StatusServiceUnavailable, "peer_unreachable"})
		return
	}
	_ = pc.SetDeadline(time.Now().Add(25 * time.Second))
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n%s: %s\r\n\r\n",
		r.Host, r.Host, r.Header.Get("Proxy-Authorization"), HopHeader, s.cfg.RelayID)
	if _, err := io.WriteString(pc, req); err != nil {
		_ = pc.Close()
		s.deny(ctx, w, scope, r.Host, denial{http.StatusBadGateway, "peer_write_failed"})
		return
	}
	br := bufio.NewReader(pc)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		_ = pc.Close()
		s.deny(ctx, w, scope, r.Host, denial{http.StatusBadGateway, "peer_bad_response"})
		return
	}
	if resp.StatusCode != http.StatusOK {
		_ = pc.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			s.cfg.Peers.Forget(scope.ConnectorID)
		}
		// The peer already audited the denial with its reason.
		http.Error(w, http.StatusText(resp.StatusCode), resp.StatusCode)
		return
	}
	_ = pc.SetDeadline(time.Time{})
	client, buffered, err := hijack(w)
	if err != nil {
		_ = pc.Close()
		return
	}
	if buffered != nil {
		if _, err := io.Copy(pc, buffered); err != nil {
			_ = pc.Close()
			_ = client.Close()
			return
		}
	}
	peerConn := &bufferedConn{Conn: pc, r: br}
	s.log.Debug("relay.forwarded_to_peer", "peer", peer, "connector_id", scope.ConnectorID)
	pipe.Bidirectional(client, peerConn)
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *bufferedConn) CloseWrite() error {
	if cw, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return b.Close()
}
