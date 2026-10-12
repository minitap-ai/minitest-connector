// Package runtoken verifies the short-lived tokens testing-service mints for
// each device egress session. A run token is an EdDSA (Ed25519) JWT scoped to
// one tenant, app, connector and egress session, and to the domains and ports
// the app is configured for. The relay verifies it locally on every CONNECT,
// so no network call sits on the hot path.
package runtoken

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/minitap-ai/minitest-connector/internal/selector"
)

const (
	// Issuer is the only accepted "iss".
	Issuer = "testing-service"
	// Audience is the only accepted "aud".
	Audience = "minitest-connector-relay"
	// MaxLifetime bounds exp - iat so a leaked token cannot live long.
	MaxLifetime = 24 * time.Hour
)

// Claims are the run token claims.
type Claims struct {
	TenantID        string   `json:"tenant_id"`
	AppID           string   `json:"app_id"`
	ConnectorID     string   `json:"connector_id"`
	EgressSessionID string   `json:"egress_session_id"`
	Domains         []string `json:"domains"`
	Ports           []int    `json:"ports,omitempty"`
	jwt.RegisteredClaims
}

// Scope is a verified token, ready to authorize a CONNECT.
type Scope struct {
	Claims
	Selectors selector.List
	PortSet   map[int]struct{}
}

// AllowsPort reports whether port is in scope.
func (s *Scope) AllowsPort(port int) bool {
	_, ok := s.PortSet[port]
	return ok
}

// Verifier verifies run tokens against a set of public keys indexed by kid.
type Verifier struct {
	keys map[string]ed25519.PublicKey
	now  func() time.Time
}

// NewVerifier returns a verifier for keys.
func NewVerifier(keys map[string]ed25519.PublicKey) *Verifier {
	return &Verifier{keys: keys, now: time.Now}
}

// ParseKeys parses "kid:base64(raw 32-byte public key)" pairs separated by commas.
func ParseKeys(spec string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, b64, ok := strings.Cut(entry, ":")
		if !ok || kid == "" {
			return nil, fmt.Errorf("run token key %q: want kid:base64", entry)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("run token key %q: invalid Ed25519 public key", kid)
		}
		keys[kid] = ed25519.PublicKey(raw)
	}
	if len(keys) == 0 {
		return nil, errors.New("no run token keys configured")
	}
	return keys, nil
}

// ErrInvalid wraps every verification failure.
var ErrInvalid = errors.New("invalid run token")

// Verify checks signature, issuer, audience, lifetime and required claims.
func (v *Verifier) Verify(token string) (*Scope, error) {
	var claims Claims
	parsed, err := jwt.ParseWithClaims(token, &claims, v.key,
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(Issuer),
		jwt.WithAudience(Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if claims.IssuedAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > MaxLifetime {
		return nil, fmt.Errorf("%w: lifetime exceeds %s", ErrInvalid, MaxLifetime)
	}
	if claims.TenantID == "" || claims.ConnectorID == "" || claims.EgressSessionID == "" {
		return nil, fmt.Errorf("%w: missing tenant, connector or egress session", ErrInvalid)
	}
	sel, err := selector.Parse(claims.Domains)
	if err != nil {
		return nil, fmt.Errorf("%w: domains: %v", ErrInvalid, err)
	}
	ports := claims.Ports
	if len(ports) == 0 {
		ports = []int{443}
	}
	portSet := make(map[int]struct{}, len(ports))
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("%w: invalid port %d", ErrInvalid, p)
		}
		portSet[p] = struct{}{}
	}
	return &Scope{Claims: claims, Selectors: sel, PortSet: portSet}, nil
}

func (v *Verifier) key(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	key, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("unknown kid %q", kid)
	}
	return key, nil
}

// Sign mints a token. testing-service is the production signer; this exists
// for tests and local development.
func Sign(claims Claims, kid string, priv ed25519.PrivateKey) (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	t.Header["kid"] = kid
	return t.SignedString(priv)
}
