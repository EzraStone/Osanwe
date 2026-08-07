package bearer

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// A loopback server is not a private server.
//
// Every page you visit can make requests to 127.0.0.1. If bearer accepted
// them, any website could spend your tokens, read which relay you are on, and
// send prompts as you -- from a tab you forgot was open. Being bound to
// loopback stops other machines reaching it and does nothing about the browser
// already running on this one.
//
// Three checks, all default-deny:
//
//   - Host must name loopback. This is what stops DNS rebinding, where an
//     attacker points evil.com at 127.0.0.1 so their page becomes same-origin
//     with this server. The Origin check below would pass in that case; the
//     Host header still says evil.com.
//   - Origin, when present, must be loopback.
//   - Sec-Fetch-Site must not say cross-site.
//
// # Why a missing Origin is allowed
//
// It is tempting to require an Origin header and reject anything without one.
// That would be wrong twice over. Legitimate clients -- the Anthropic SDK,
// curl, a Python script -- never send one, so requiring it breaks every real
// user. And it buys nothing, because a browser making a cross-origin request
// always sends Origin. A request with no Origin did not come from a page
// attacking you.
//
// So absence is safe and presence is checked. Anyone tempted to "fix" this by
// making Origin mandatory should read that paragraph again first.

// ErrCrossOrigin describes why a request was refused.
type originError struct{ reason string }

func (e *originError) Error() string { return e.reason }

// checkOrigin reports why a request must be refused, or nil.
func (s *Server) checkOrigin(r *http.Request) error {
	// Sec-Fetch-Site is sent by current browsers and is the most direct signal
	// available: the browser itself says whether this crossed a site boundary.
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		// same-site is also refused. A subdomain of a site you visited is not
		// this program, and "site" is a laxer boundary than origin.
		return &originError{"request came from another site"}
	}

	if origin := r.Header.Get("Origin"); origin != "" {
		if !s.originAllowed(origin) {
			return &originError{fmt.Sprintf("origin %s is not allowed to reach this client", origin)}
		}
	}

	// Host is checked last because it is the subtlest: it catches the case
	// where an attacker's own name resolves here, which makes every
	// origin-based check agree with them.
	if !s.cfg.AllowNonLoopback && !isLoopbackHost(r.Host) {
		return &originError{fmt.Sprintf("request addressed to %q, which is not a loopback name; "+
			"a name that resolves to 127.0.0.1 but belongs to someone else is how a website reaches a local server", r.Host)}
	}
	return nil
}

// originAllowed reports whether an Origin header may talk to this client.
func (s *Server) originAllowed(origin string) bool {
	for _, allowed := range s.cfg.AllowOrigins {
		if strings.EqualFold(strings.TrimSuffix(allowed, "/"), strings.TrimSuffix(origin, "/")) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return isLoopbackHost(u.Host)
}

// isLoopbackHost reports whether a Host or authority names this machine.
func isLoopbackHost(hostPort string) bool {
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")

	if strings.EqualFold(host, "localhost") {
		return true
	}
	// A name ending in .localhost is reserved for loopback by RFC 6761, but
	// only the exact name is trusted here: resolution is not consulted, and a
	// name that merely happens to resolve to 127.0.0.1 is precisely the attack.
	if strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// refuseOrigin answers a request that failed the checks above.
func (s *Server) refuseOrigin(w http.ResponseWriter, err error) {
	s.metrics.CrossOrigin.Add(1)
	w.Header().Set("Content-Type", "application/json")
	// No CORS headers are set anywhere in this file, deliberately. Granting a
	// cross-origin read is the thing being prevented, and a permissive header
	// added later "to make the UI work" would undo all of it.
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `{"type":"error","error":{"type":"osanwe_cross_origin","message":%q}}`+"\n",
		"Refused: "+err.Error()+". Osanwë listens on loopback, which any page in your browser can also reach, so it only answers its own interface and non-browser clients.")
}
