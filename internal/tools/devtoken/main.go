// Command devtoken mints local-only dev keys and tokens; production run tokens come from testing-service.
//
//	go run ./internal/tools/devtoken -connector conn-1 -tenant tenant-1 -domains example.com
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/minitap-ai/minitest-connector/internal/relay"
	"github.com/minitap-ai/minitest-connector/internal/runtoken"
)

func main() {
	tenant := flag.String("tenant", "tenant-1", "tenant id")
	connector := flag.String("connector", "conn-1", "connector id")
	domains := flag.String("domains", "example.com", "comma-separated domains")
	connectorToken := flag.String("connector-token", "mtc_dev_localdevtoken0000000000000000000000", "connector token to register")
	flag.Parse()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	now := time.Now()
	tok, err := runtoken.Sign(runtoken.Claims{
		TenantID: *tenant, AppID: "app-dev", ConnectorID: *connector, EgressSessionID: "egress-dev",
		Domains: strings.Split(*domains, ","),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: runtoken.Issuer, Audience: jwt.ClaimStrings{runtoken.Audience},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}, "dev", priv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("RELAY_RUN_TOKEN_KEYS=dev:%s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Printf("RELAY_STATIC_CONNECTORS=%s:%s:%s\n", *tenant, *connector, relay.HashToken(*connectorToken))
	fmt.Printf("MINITEST_CONNECTOR_TOKEN=%s\n", *connectorToken)
	fmt.Printf("RUN_TOKEN=%s\n", tok)
}
