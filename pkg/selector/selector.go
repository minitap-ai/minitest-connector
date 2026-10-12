// Package selector parses and matches domain selectors. The syntax is kept
// identical to mobile-use-cli/capture/internal/egress (Limrun SDK rules) so the
// web app, the egress agent, the relay and the connector agree on every domain:
// "api.example.com" matches that host only, "*.example.com" matches its
// subdomains but not example.com itself.
package selector

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

const (
	// MaxSelectors bounds a single list, as the Limrun SDK does.
	MaxSelectors   = 64
	wildcardPrefix = "*."
)

var labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// List is a canonical, de-duplicated set of selectors.
type List []string

// Parse canonicalizes raw selectors. Entries may also be comma separated.
func Parse(raw []string) (List, error) {
	var values []string
	for _, entry := range raw {
		for part := range strings.SplitSeq(entry, ",") {
			if strings.TrimSpace(part) != "" {
				values = append(values, part)
			}
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one domain is required")
	}
	if len(values) > MaxSelectors {
		return nil, fmt.Errorf("at most %d domains are allowed, got %d", MaxSelectors, len(values))
	}
	seen := make(map[string]struct{}, len(values))
	out := make(List, 0, len(values))
	for _, value := range values {
		canonical, err := canonicalize(value)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out, nil
}

// Matches reports whether host is covered by at least one selector.
func (l List) Matches(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, s := range l {
		if matches(s, host) {
			return true
		}
	}
	return false
}

func canonicalize(value string) (string, error) {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	base := strings.TrimPrefix(domain, wildcardPrefix)
	if base == "" || len(base) > 253 {
		return "", fmt.Errorf("invalid domain %q", value)
	}
	if base == "localhost" || net.ParseIP(base) != nil {
		return "", fmt.Errorf("%q must be a domain name", value)
	}
	labels := strings.Split(base, ".")
	for _, label := range labels {
		if len(label) > 63 || !labelPattern.MatchString(label) {
			return "", fmt.Errorf("invalid domain %q", value)
		}
	}
	if isNumeric(labels[len(labels)-1]) {
		return "", fmt.Errorf("domain %q has a numeric top-level label", value)
	}
	return domain, nil
}

func isNumeric(label string) bool {
	for _, r := range label {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func matches(selector, host string) bool {
	if base, ok := strings.CutPrefix(selector, wildcardPrefix); ok {
		return len(host) > len(base)+1 && strings.HasSuffix(host, "."+base)
	}
	return host == selector
}
