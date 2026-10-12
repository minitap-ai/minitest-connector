package relay

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// PeerFinder locates the relay pod holding a connector session.
type PeerFinder interface {
	// Find returns the internal host:port of a peer holding connectorID.
	Find(ctx context.Context, connectorID string) (string, bool)
	// Forget drops a cached location that turned out stale.
	Forget(connectorID string)
}

// DNSPeers discovers peers through a Kubernetes headless Service: every pod IP
// behind it is asked, in parallel, whether it holds the connector. Positive
// answers are cached briefly; misses are not, so a reconnecting connector is
// found again right away.
type DNSPeers struct {
	Service string // headless service DNS name
	Port    int    // internal listener port
	SelfIP  string // this pod's IP, skipped
	TTL     time.Duration

	Resolver *net.Resolver
	Client   *http.Client

	mu    sync.Mutex
	cache map[string]cachedPeer
}

type cachedPeer struct {
	addr    string
	expires time.Time
}

// Find implements PeerFinder.
func (p *DNSPeers) Find(ctx context.Context, connectorID string) (string, bool) {
	if addr, ok := p.cached(connectorID); ok {
		return addr, true
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := resolver.LookupHost(lookupCtx, p.Service)
	if err != nil {
		return "", false
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}

	found := make(chan string, len(ips))
	var wg sync.WaitGroup
	for _, ip := range ips {
		if ip == p.SelfIP {
			continue
		}
		addr := net.JoinHostPort(ip, strconv.Itoa(p.Port))
		wg.Add(1)
		go func() {
			defer wg.Done()
			// addr comes from the cluster DNS of our own headless service, never from a request.
			req, err := http.NewRequestWithContext(lookupCtx, http.MethodGet, //nolint:gosec // G704: see above
				"http://"+addr+"/internal/v1/connectors/"+url.PathEscape(connectorID), nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req) //nolint:gosec // G704: internal peer address
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				found <- addr
			}
		}()
	}
	go func() {
		wg.Wait()
		close(found)
	}()
	addr, ok := <-found
	if ok {
		p.store(connectorID, addr)
	}
	return addr, ok
}

// Forget implements PeerFinder.
func (p *DNSPeers) Forget(connectorID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cache, connectorID)
}

func (p *DNSPeers) cached(id string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.cache[id]
	if !ok || time.Now().After(c.expires) {
		return "", false
	}
	return c.addr, true
}

func (p *DNSPeers) store(id, addr string) {
	ttl := p.TTL
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = map[string]cachedPeer{}
	}
	p.cache[id] = cachedPeer{addr: addr, expires: time.Now().Add(ttl)}
}
