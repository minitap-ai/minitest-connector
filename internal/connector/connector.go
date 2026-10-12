// Package connector is the customer-side agent. It keeps a small pool of
// outbound sessions to the relay and serves the streams the relay opens,
// dialing only hosts and ports the customer allowed.
package connector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/minitap-ai/minitest-connector/internal/netguard"
	"github.com/minitap-ai/minitest-connector/internal/selector"
)

// DefaultRelayURL is the production relay.
const DefaultRelayURL = "wss://connect.minitap.ai/v1/connect"

// DialFunc dials a target inside the customer network.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Config configures a connector instance.
type Config struct {
	RelayURL   string
	Token      string
	Allow      selector.List
	Ports      []int
	Sessions   int // concurrent relay sessions, default 2
	Version    string
	InstanceID string
	Hostname   string
	Logger     *slog.Logger

	// Dial defaults to a dialer refusing loopback, link-local and metadata
	// addresses. Tests override it.
	Dial DialFunc
	// WSDialer defaults to one honouring HTTPS_PROXY.
	WSDialer *websocket.Dialer
}

// Connector runs the relay sessions.
type Connector struct {
	cfg   Config
	log   *slog.Logger
	ports map[int]struct{}

	mu      sync.Mutex
	relayOf map[int]string // session slot -> relay pod id
	online  map[int]bool
}

// New validates cfg and returns a connector.
func New(cfg Config) (*Connector, error) {
	if cfg.Token == "" {
		return nil, errors.New("a connector token is required")
	}
	if len(cfg.Allow) == 0 {
		return nil, errors.New("at least one allowed domain is required")
	}
	if cfg.RelayURL == "" {
		cfg.RelayURL = DefaultRelayURL
	}
	if cfg.Sessions <= 0 {
		cfg.Sessions = 2
	}
	if len(cfg.Ports) == 0 {
		cfg.Ports = []int{443}
	}
	ports := make(map[int]struct{}, len(cfg.Ports))
	for _, p := range cfg.Ports {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %d", p)
		}
		ports[p] = struct{}{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Dial == nil {
		cfg.Dial = netguard.Dialer().DialContext
	}
	if cfg.WSDialer == nil {
		cfg.WSDialer = &websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 15 * time.Second}
	}
	return &Connector{cfg: cfg, log: cfg.Logger, ports: ports, relayOf: map[int]string{}, online: map[int]bool{}}, nil
}

// Online reports how many sessions are currently established.
func (c *Connector) Online() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ok := range c.online {
		if ok {
			n++
		}
	}
	return n
}

// Run keeps cfg.Sessions sessions alive until ctx is cancelled.
func (c *Connector) Run(ctx context.Context) error {
	c.log.Info("connector.starting", "relay", c.cfg.RelayURL, "allow", []string(c.cfg.Allow),
		"ports", c.cfg.Ports, "sessions", c.cfg.Sessions, "version", c.cfg.Version)
	var wg sync.WaitGroup
	for slot := range c.cfg.Sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.slotLoop(ctx, slot)
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func (c *Connector) slotLoop(ctx context.Context, slot int) {
	backoff := time.Second
	sameRelayRetries := 0
	for ctx.Err() == nil {
		s, err := c.dial(ctx)
		if err != nil {
			wait := backoff
			var authErr *AuthError
			if errors.As(err, &authErr) {
				wait = time.Minute
				c.log.Error("connector.unauthorized", "slot", slot, "status", authErr.Status,
					"hint", "check the connector token; it may have been revoked or rotated")
			} else {
				c.log.Warn("connector.connect_failed", "slot", slot, "error", err, "retry_in", wait.String())
			}
			sleep(ctx, jitter(wait))
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second

		if c.sharesRelay(slot, s.relayID) && sameRelayRetries < 3 {
			// Both sessions on one relay pod would make that pod a single point
			// of failure: try to land elsewhere.
			sameRelayRetries++
			s.close()
			sleep(ctx, jitter(500*time.Millisecond))
			continue
		}
		sameRelayRetries = 0
		c.setOnline(slot, s.relayID, true)
		c.log.Info("connector.session_established", "slot", slot, "session_id", s.sessionID, "relay_id", s.relayID)

		reason := s.serve(ctx, c)
		c.setOnline(slot, "", false)
		c.log.Info("connector.session_ended", "slot", slot, "session_id", s.sessionID, "reason", reason)
		if reason != reasonGoAway {
			sleep(ctx, jitter(time.Second))
		}
	}
}

func (c *Connector) sharesRelay(slot int, relayID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for other, id := range c.relayOf {
		if other != slot && c.online[other] && id == relayID {
			return true
		}
	}
	return false
}

func (c *Connector) setOnline(slot int, relayID string, online bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.online[slot] = online
	c.relayOf[slot] = relayID
}

func (c *Connector) allowsPort(p int) bool {
	_, ok := c.ports[p]
	return ok
}

func (c *Connector) allowList() []string { return slices.Clone(c.cfg.Allow) }

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d))) //nolint:gosec // jitter, not security
}
