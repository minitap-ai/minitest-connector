package runtoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func validClaims() Claims {
	now := time.Now()
	return Claims{
		TenantID: "t1", AppID: "a1", ConnectorID: "c1", EgressSessionID: "e1",
		Domains: []string{"*.stg.example.local"},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: Issuer, Audience: jwt.ClaimStrings{Audience},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
}

func TestVerify(t *testing.T) {
	pub, priv := newKey(t)
	v := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	tok, err := Sign(validClaims(), "k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := v.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Selectors.Matches("web.stg.example.local") || !scope.AllowsPort(443) || scope.AllowsPort(80) {
		t.Fatalf("unexpected scope %+v", scope)
	}
}

func TestVerifyRejects(t *testing.T) {
	pub, priv := newKey(t)
	_, otherPriv := newKey(t)
	v := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})

	mutate := map[string]func(*Claims) (string, ed25519.PrivateKey){
		"wrong key":   func(c *Claims) (string, ed25519.PrivateKey) { return "k1", otherPriv },
		"unknown kid": func(c *Claims) (string, ed25519.PrivateKey) { return "k2", priv },
		"expired": func(c *Claims) (string, ed25519.PrivateKey) {
			c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
			return "k1", priv
		},
		"wrong audience": func(c *Claims) (string, ed25519.PrivateKey) {
			c.Audience = jwt.ClaimStrings{"other"}
			return "k1", priv
		},
		"wrong issuer": func(c *Claims) (string, ed25519.PrivateKey) { c.Issuer = "someone"; return "k1", priv },
		"too long": func(c *Claims) (string, ed25519.PrivateKey) {
			c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(48 * time.Hour))
			return "k1", priv
		},
		"no tenant":  func(c *Claims) (string, ed25519.PrivateKey) { c.TenantID = ""; return "k1", priv },
		"bad domain": func(c *Claims) (string, ed25519.PrivateKey) { c.Domains = []string{"10.0.0.1"}; return "k1", priv },
		"bad port":   func(c *Claims) (string, ed25519.PrivateKey) { c.Ports = []int{70000}; return "k1", priv },
	}
	for name, m := range mutate {
		c := validClaims()
		kid, key := m(&c)
		tok, err := Sign(c, kid, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Verify(tok); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestVerifyRejectsHS256(t *testing.T) {
	pub, _ := newKey(t)
	v := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims())
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString([]byte(pub))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(s); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestParseKeys(t *testing.T) {
	pub, _ := newKey(t)
	keys, err := ParseKeys("k1:" + base64.StdEncoding.EncodeToString(pub))
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if _, err := ParseKeys("k1:not-base64"); err == nil {
		t.Fatal("want error")
	}
}
