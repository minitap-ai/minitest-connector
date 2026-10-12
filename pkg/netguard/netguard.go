// Package netguard refuses connections to addresses a connector must never
// reach on behalf of Minitap, whatever a domain resolves to: loopback (the
// connector host itself), link-local (cloud metadata services), multicast and
// unspecified addresses. Private ranges stay allowed: reaching them is the
// whole point of the connector.
//
// The check runs in the dialer's Control hook, on the address actually being
// connected to, so DNS rebinding between resolution and dial cannot bypass it.
package netguard

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS IPv6 metadata
}

// Allowed reports whether a connector may dial addr.
func Allowed(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// ErrBlocked is returned when the target address is not allowed.
type ErrBlocked struct{ Addr string }

func (e *ErrBlocked) Error() string { return fmt.Sprintf("address %s is not allowed", e.Addr) }

// Control is a net.Dialer Control hook enforcing Allowed.
func Control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &ErrBlocked{Addr: address}
	}
	if !Allowed(ap.Addr()) {
		return &ErrBlocked{Addr: ap.Addr().String()}
	}
	return nil
}

// Dialer returns a dialer that enforces Allowed on every connection attempt.
func Dialer() *net.Dialer {
	return &net.Dialer{Control: Control}
}
