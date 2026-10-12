package relay_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/minitap-ai/minitest-connector/internal/connector"
	"github.com/minitap-ai/minitest-connector/internal/relay"
	"github.com/minitap-ai/minitest-connector/internal/runtoken"
	"github.com/minitap-ai/minitest-connector/internal/selector"
)

const connectorToken = "mtc_test_abcdefghijklmnopqrstuvwxyz0123456789"

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t        *testing.T
	priv     ed25519.PrivateKey
	verifier *runtoken.Verifier
	auth     relay.Authenticator
	target   string // echo server address
	logs     *syncBuffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := relay.ParseStatic("tenant-1:conn-1:" + relay.HashToken(connectorToken))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return &harness{
		t: t, priv: priv, auth: auth, target: ln.Addr().String(), logs: &syncBuffer{},
		verifier: runtoken.NewVerifier(map[string]ed25519.PublicKey{"k1": pub}),
	}
}

type relayPod struct {
	srv      *relay.Server
	public   *httptest.Server
	internal *httptest.Server
}

func (h *harness) startRelay(id string, peers relay.PeerFinder) *relayPod {
	srv := relay.New(relay.Config{
		RelayID: id, Auth: h.auth, Tokens: h.verifier, Peers: peers,
		Logger: slog.New(slog.NewJSONHandler(h.logs, nil)),
	})
	p := &relayPod{srv: srv, public: httptest.NewServer(srv.PublicHandler()), internal: httptest.NewServer(srv.InternalHandler(nil))}
	h.t.Cleanup(func() {
		p.public.CloseClientConnections()
		p.public.Close()
		p.internal.Close()
	})
	return p
}

func (h *harness) startConnector(pod *relayPod, sessions int, allow ...string) *connector.Connector {
	sel, err := selector.Parse(allow)
	if err != nil {
		h.t.Fatal(err)
	}
	// Every allowed hostname resolves to the local echo server.
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, h.target)
	}
	c, err := connector.New(connector.Config{
		RelayURL: "ws" + strings.TrimPrefix(pod.public.URL, "http") + "/v1/connect",
		Token:    connectorToken, Allow: sel, Sessions: sessions, Version: "test",
		InstanceID: "inst-1", Hostname: "qa-mac-mini-01", Dial: dial,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()
	waitFor(h.t, func() bool { return c.Online() == sessions })
	return c
}

func (h *harness) token(connectorID string, domains ...string) string {
	now := time.Now()
	tok, err := runtoken.Sign(runtoken.Claims{
		TenantID: "tenant-1", AppID: "app-1", ConnectorID: connectorID, EgressSessionID: "egress-1",
		Domains: domains,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: runtoken.Issuer, Audience: jwt.ClaimStrings{runtoken.Audience},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}, "k1", h.priv)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// connect sends a CONNECT through addr and returns the status and, on 200,
// the tunnel connection.
func connect(t *testing.T, addr, target, token string) (int, net.Conn) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n\r\n", target, target, token)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = c.Close()
		return resp.StatusCode, nil
	}
	return resp.StatusCode, c
}

func echo(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func addr(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }

func TestTunnelEndToEnd(t *testing.T) {
	h := newHarness(t)
	pod := h.startRelay("relay-a", nil)
	h.startConnector(pod, 2, "*.stg.example.local")

	status, c := connect(t, addr(pod.internal), "web.stg.example.local:443", h.token("conn-1", "*.stg.example.local"))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	echo(t, c, "hello through the tunnel")
	_ = c.Close()

	waitFor(t, func() bool { return strings.Contains(h.logs.String(), `"event":"private_network.stream_closed"`) })
	for _, want := range []string{
		`"event":"private_network.instance_connected"`, `"hostname":"qa-mac-mini-01"`,
		`"event":"private_network.stream_opened"`, `"egress_session_id":"egress-1"`,
	} {
		if !strings.Contains(h.logs.String(), want) {
			t.Errorf("audit log missing %s", want)
		}
	}
	if strings.Contains(h.logs.String(), connectorToken) {
		t.Error("audit log leaked the connector token")
	}
}

func TestTunnelDenials(t *testing.T) {
	h := newHarness(t)
	pod := h.startRelay("relay-a", nil)
	h.startConnector(pod, 1, "*.stg.example.local")

	cases := []struct {
		name, target, token string
		status              int
		reason              string
	}{
		{"invalid token", "web.stg.example.local:443", "garbage", http.StatusProxyAuthRequired, "invalid_run_token"},
		{"host outside app config", "evil.example.com:443", h.token("conn-1", "*.stg.example.local"), http.StatusForbidden, "domain_not_in_app_config"},
		{"port outside app config", "web.stg.example.local:22", h.token("conn-1", "*.stg.example.local"), http.StatusForbidden, "port_not_in_app_config"},
		{"host refused by connector", "api.other.local:443", h.token("conn-1", "api.other.local"), http.StatusForbidden, "domain_not_allowed_by_connector"},
		{"connector offline", "web.stg.example.local:443", h.token("conn-2", "*.stg.example.local"), http.StatusServiceUnavailable, "connector_offline"},
	}
	for _, tc := range cases {
		status, c := connect(t, addr(pod.internal), tc.target, tc.token)
		if c != nil {
			_ = c.Close()
		}
		if status != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.name, status, tc.status)
		}
		if !strings.Contains(h.logs.String(), `"reason":"`+tc.reason+`"`) {
			t.Errorf("%s: no audit event with reason %s", tc.name, tc.reason)
		}
	}
}

func TestConnectorRejectsBadToken(t *testing.T) {
	h := newHarness(t)
	pod := h.startRelay("relay-a", nil)
	sel, _ := selector.Parse([]string{"*.stg.example.local"})
	c, err := connector.New(connector.Config{
		RelayURL: "ws" + strings.TrimPrefix(pod.public.URL, "http") + "/v1/connect",
		Token:    "mtc_test_wrong", Allow: sel, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	waitFor(t, func() bool { return strings.Contains(h.logs.String(), `"reason":"invalid_token"`) })
	if c.Online() != 0 {
		t.Fatal("connector online with a bad token")
	}
}

type staticPeers struct{ addr string }

func (p staticPeers) Find(context.Context, string) (string, bool) { return p.addr, true }
func (p staticPeers) Forget(string)                               {}

func TestForwardToPeer(t *testing.T) {
	h := newHarness(t)
	owner := h.startRelay("relay-a", nil)
	h.startConnector(owner, 1, "*.stg.example.local")
	other := h.startRelay("relay-b", staticPeers{addr: addr(owner.internal)})

	status, c := connect(t, addr(other.internal), "web.stg.example.local:443", h.token("conn-1", "*.stg.example.local"))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	echo(t, c, "hop")
	_ = c.Close()
}

func TestDrainKeepsInflightStreams(t *testing.T) {
	h := newHarness(t)
	pod := h.startRelay("relay-a", nil)
	h.startConnector(pod, 1, "*.stg.example.local")

	status, c := connect(t, addr(pod.internal), "web.stg.example.local:443", h.token("conn-1", "*.stg.example.local"))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	drained := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pod.srv.Drain(ctx)
		close(drained)
	}()
	waitFor(t, func() bool { return !pod.srv.HasConnector("conn-1") })
	echo(t, c, "still flowing during drain")
	select {
	case <-drained:
		t.Fatal("drain returned while a stream was open")
	case <-time.After(200 * time.Millisecond):
	}
	_ = c.Close()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish after the stream closed")
	}
}
