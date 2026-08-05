// Package policy decides which destinations a ranger is willing to carry
// traffic to.
//
// The rule is default-deny, and it is not configurable. An open CONNECT proxy
// on a public address is found by scanners within hours and becomes someone
// else's abuse relay; the operator then carries the consequences for traffic
// they cannot even read. A ranger therefore forwards only to destinations its
// operator listed explicitly.
package policy

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// DefaultPort is assumed when an allowlist entry omits one. Everything a
// ranger carries is TLS, so 443 is the only sensible default.
const DefaultPort = 443

// Destination is a host and port a ranger may connect to.
type Destination struct {
	Host string // lowercase, no trailing dot
	Port int
}

func (d Destination) String() string {
	return net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
}

// Allowlist is an immutable set of permitted destinations. The zero value
// permits nothing, which is the correct behaviour for a misconfigured relay:
// refuse everything rather than forward anything.
type Allowlist struct {
	allowed map[Destination]struct{}
}

// Parse builds an Allowlist from entries of the form "host" or "host:port".
// A bare host is taken to mean host:443.
//
// An empty entry list is an error rather than an empty allowlist. Starting a
// relay that silently refuses every request looks identical to a network
// outage, and an operator debugging that would reasonably conclude the
// software is broken.
func Parse(entries []string) (*Allowlist, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("policy: no destinations allowed; a ranger with an empty allowlist would refuse every request")
	}

	set := make(map[Destination]struct{}, len(entries))
	for _, raw := range entries {
		d, err := ParseDestination(raw)
		if err != nil {
			return nil, err
		}
		set[d] = struct{}{}
	}
	return &Allowlist{allowed: set}, nil
}

// ParseDestination parses a single "host" or "host:port" entry.
func ParseDestination(raw string) (Destination, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Destination{}, fmt.Errorf("policy: empty destination")
	}

	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		// No port present (or a bare IPv6 literal). Treat the whole string as
		// a host and apply the default port.
		host, portStr = s, strconv.Itoa(DefaultPort)
	}

	host = normalizeHost(host)
	if host == "" {
		return Destination{}, fmt.Errorf("policy: %q has no host", raw)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return Destination{}, fmt.Errorf("policy: %q has a non-numeric port %q", raw, portStr)
	}
	if port < 1 || port > 65535 {
		return Destination{}, fmt.Errorf("policy: %q has port %d outside 1-65535", raw, port)
	}

	return Destination{Host: host, Port: port}, nil
}

// normalizeHost lowercases, strips the root label's trailing dot, and removes
// IPv6 brackets. Without this, "API.Anthropic.com", "api.anthropic.com." and
// "api.anthropic.com" would be three different allowlist entries and an
// operator could believe a destination is blocked when it is not.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	h = strings.ToLower(h)
	for strings.HasSuffix(h, ".") {
		h = strings.TrimSuffix(h, ".")
	}
	return h
}

// Allows reports whether the destination in "host:port" form may be dialled.
// A malformed target is refused rather than reported as an error, because
// from the caller's point of view both outcomes are the same 403 and there is
// nothing useful to say about a request that was never valid.
func (a *Allowlist) Allows(hostPort string) bool {
	if a == nil || len(a.allowed) == 0 {
		return false
	}
	d, err := ParseDestination(hostPort)
	if err != nil {
		return false
	}
	// A bare host in a CONNECT target is not valid; require an explicit port
	// so "api.anthropic.com" cannot be smuggled past a :443-only allowlist.
	if _, _, err := net.SplitHostPort(strings.TrimSpace(hostPort)); err != nil {
		return false
	}
	_, ok := a.allowed[d]
	return ok
}

// Destinations returns the permitted destinations in sorted order, for logging
// at startup so an operator can see what they actually configured.
func (a *Allowlist) Destinations() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.allowed))
	for d := range a.allowed {
		out = append(out, d.String())
	}
	sort.Strings(out)
	return out
}

// Len reports how many destinations are permitted.
func (a *Allowlist) Len() int {
	if a == nil {
		return 0
	}
	return len(a.allowed)
}
