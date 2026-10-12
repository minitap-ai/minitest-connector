package relay

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Identity is who a connector token belongs to.
type Identity struct {
	TenantID    string
	ConnectorID string
}

// ErrUnauthorized is returned for unknown, revoked or malformed tokens.
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator resolves a connector token to its identity.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Identity, error)
}

// TokenPrefix returns the non-secret prefix of a token, safe to log
// ("mtc_live_ab12" for "mtc_live_ab12…").
func TokenPrefix(token string) string {
	i := strings.LastIndex(token, "_")
	if i < 0 || len(token) < i+5 {
		return ""
	}
	return token[:i+5]
}

// HashToken is how tokens are stored: hex SHA-256. Tokens are 256-bit random
// values, so a fast hash is enough.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// StaticAuthenticator authenticates against a fixed list of token hashes. It
// exists for local development and tests; deployed relays authenticate
// against testing-service.
type StaticAuthenticator struct {
	entries []staticEntry
}

type staticEntry struct {
	hash []byte
	id   Identity
}

// ParseStatic parses "tenant_id:connector_id:sha256hex" entries separated by commas.
func ParseStatic(spec string) (*StaticAuthenticator, error) {
	a := &StaticAuthenticator{}
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("static connector %q: want tenant:connector:sha256", entry)
		}
		hash, err := hex.DecodeString(parts[2])
		if err != nil || len(hash) != sha256.Size {
			return nil, fmt.Errorf("static connector %q: invalid sha256", parts[1])
		}
		a.entries = append(a.entries, staticEntry{hash: hash, id: Identity{TenantID: parts[0], ConnectorID: parts[1]}})
	}
	return a, nil
}

// Authenticate implements Authenticator.
func (a *StaticAuthenticator) Authenticate(_ context.Context, token string) (Identity, error) {
	sum := sha256.Sum256([]byte(token))
	for _, e := range a.entries {
		if subtle.ConstantTimeCompare(sum[:], e.hash) == 1 {
			return e.id, nil
		}
	}
	return Identity{}, ErrUnauthorized
}
