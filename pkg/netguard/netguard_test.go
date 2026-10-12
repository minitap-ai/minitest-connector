package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestAllowed(t *testing.T) {
	cases := map[string]bool{
		"10.12.0.15":       true,
		"192.168.1.10":     true,
		"172.16.4.4":       true,
		"8.8.8.8":          true,
		"fd12::1":          true,
		"127.0.0.1":        false,
		"::1":              false,
		"169.254.169.254":  false,
		"fe80::1":          false,
		"0.0.0.0":          false,
		"::":               false,
		"224.0.0.1":        false,
		"::ffff:127.0.0.1": false,
		"100.100.100.200":  false,
		"fd00:ec2::254":    false,
	}
	for s, want := range cases {
		if got := Allowed(netip.MustParseAddr(s)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestDialerRefusesLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, err = Dialer().DialContext(context.Background(), "tcp", ln.Addr().String())
	var blocked *ErrBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("dial error = %v, want ErrBlocked", err)
	}
}
