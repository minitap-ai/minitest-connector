package connector

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/minitap-ai/minitest-connector/pkg/selector"
	"github.com/minitap-ai/minitest-connector/pkg/wire"
)

// fakeRelay accepts one connector session and hands it to the test.
type fakeRelay struct {
	srv      *httptest.Server
	sessions chan *yamux.Session
	hellos   chan wire.Hello
}

func newFakeRelay(t *testing.T, token string) *fakeRelay {
	t.Helper()
	r := &fakeRelay{sessions: make(chan *yamux.Session, 4), hellos: make(chan wire.Hello, 4)}
	up := websocket.Upgrader{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		ys, err := yamux.Server(wire.NewWSConn(ws), wire.YamuxConfig())
		if err != nil {
			return
		}
		ctrl, err := ys.AcceptStream()
		if err != nil {
			return
		}
		var hello wire.Hello
		if wire.Read(ctrl, &hello) != nil {
			return
		}
		_ = wire.Write(ctrl, wire.HelloAck{SessionID: "s1", RelayID: "relay-a", ConnectorID: "c1"})
		r.hellos <- hello
		r.sessions <- ys
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRelay) url() string { return "ws" + strings.TrimPrefix(r.srv.URL, "http") }

func startConnector(t *testing.T, relayURL, token string, dial DialFunc) {
	t.Helper()
	allow, err := selector.Parse([]string{"*.stg.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{
		RelayURL: relayURL, Token: token, Allow: allow, Sessions: 1,
		Version: "test", InstanceID: "i1", Hostname: "host",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dial:   dial,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()
}

func openStream(t *testing.T, ys *yamux.Session, host string, port int) (net.Conn, wire.StreamReply) {
	t.Helper()
	st, err := ys.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := wire.Write(st, wire.StreamOpen{StreamID: "x", Host: host, Port: port}); err != nil {
		t.Fatal(err)
	}
	var reply wire.StreamReply
	if err := wire.Read(st, &reply); err != nil {
		t.Fatal(err)
	}
	return st, reply
}

func TestConnectorServesAllowedHostsOnly(t *testing.T) {
	relay := newFakeRelay(t, "tok")
	dialed := make(chan string, 4)
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed <- address
		local, remote := net.Pipe()
		go func() { _, _ = io.Copy(remote, remote) }() // echo
		return local, nil
	}
	startConnector(t, relay.url(), "tok", dial)

	var ys *yamux.Session
	select {
	case ys = <-relay.sessions:
	case <-time.After(5 * time.Second):
		t.Fatal("connector never connected")
	}
	hello := <-relay.hellos
	if hello.ProtocolVersion != wire.ProtocolVersion || len(hello.Allow) != 1 || hello.Allow[0] != "*.stg.example.com" {
		t.Fatalf("unexpected hello: %+v", hello)
	}

	st, reply := openStream(t, ys, "api.stg.example.com", 443)
	if !reply.OK {
		t.Fatalf("allowed host refused: %+v", reply)
	}
	if got := <-dialed; got != "api.stg.example.com:443" {
		t.Fatalf("dialed %q", got)
	}
	if _, err := st.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(st, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q %v", buf, err)
	}

	for _, tc := range []struct {
		host string
		port int
		code string
	}{
		{"stg.example.com", 443, wire.CodeDeniedDomain}, // wildcard covers subdomains only
		{"evil.example.org", 443, wire.CodeDeniedDomain},
		{"api.stg.example.com", 22, wire.CodeDeniedPort},
		{"", 443, wire.CodeBadRequest},
	} {
		_, reply := openStream(t, ys, tc.host, tc.port)
		if reply.OK || reply.Code != tc.code {
			t.Errorf("%s:%d: got %+v, want code %s", tc.host, tc.port, reply, tc.code)
		}
	}
	select {
	case got := <-dialed:
		t.Fatalf("denied target was dialed: %s", got)
	default:
	}
}

func TestConnectorRejectsBadToken(t *testing.T) {
	relay := newFakeRelay(t, "good")
	c, _ := New(Config{RelayURL: relay.url(), Token: "bad", Allow: selector.List{}, Sessions: 1})
	if c != nil {
		t.Fatal("empty allow list must be rejected")
	}
	allow, _ := selector.Parse([]string{"*.stg.example.com"})
	c, err := New(Config{RelayURL: relay.url(), Token: "bad", Allow: allow, Sessions: 1,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.dial(context.Background())
	if _, ok := err.(*AuthError); !ok {
		t.Fatalf("want AuthError, got %v", err)
	}
}
