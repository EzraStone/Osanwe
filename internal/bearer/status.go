package bearer

import (
	"encoding/json"
	"net/http"
	"time"
)

// Prefix is reserved for bearer's own routes. Everything outside it is
// forwarded.
//
// A prefix is used rather than a second listener so that an interface served
// here is same-origin with the endpoint it talks to. Two ports would mean
// every call from the page was cross-origin, which would mean relaxing exactly
// the protection in guard.go to make the interface work.
//
// Providers namespace their APIs under /v1, so nothing real collides with this.
const Prefix = "/_osanwe/"

// RelayStatus is what a client can say about its connection. internal/pool
// satisfies it; a manually pinned relay does not, and reports less.
type RelayStatus interface {
	Current() (nickname, address string, ok bool)
	Len() int
	GuardSince() (time.Time, bool)
	SignedBy() int
}

// WalletStatus is the part of a TokenSource that can be shown. Not every
// TokenSource has to offer it.
type WalletStatus interface {
	Len() int
	Spent() uint64
}

// Status is what the interface reads.
//
// Everything here is a count, a name or a yes/no. There is deliberately no
// history, no per-request record and nothing derived from a prompt: an
// endpoint that could answer "what did I ask yesterday" would be a log of the
// user's own prompts, which is the record this whole system avoids creating
// anywhere else. Secrets, tokens and pins are likewise absent -- a page that
// could read them would be a page that could spend them.
type Status struct {
	Endpoint string `json:"endpoint"`
	Upstream string `json:"upstream"`

	// Paying is "tokens" or "your own key".
	Paying string `json:"paying"`

	Relay     *RelayInfo     `json:"relay,omitempty"`
	Directory *DirectoryInfo `json:"directory,omitempty"`
	Wallet    *WalletInfo    `json:"wallet,omitempty"`

	Requests RequestInfo `json:"requests"`

	// Retained says what this process is keeping. It is a constant, and it is
	// here so the interface can state it rather than assert it.
	Retained string `json:"retained"`
}

type RelayInfo struct {
	Nickname string `json:"nickname,omitempty"`
	Address  string `json:"address"`

	// KeyMatched reports whether the relay presented the key that was
	// published for it. A tunnel is only ever established when it does, so
	// this being true is a fact about a connection that happened, not a
	// promise about one that might.
	KeyMatched bool `json:"key_matched"`

	SinceSeconds int64 `json:"since_seconds,omitempty"`
}

type DirectoryInfo struct {
	RelaysKnown  int `json:"relays_known"`
	SignedBy     int `json:"signed_by"`
	Failovers    int64
	RefreshError bool `json:"refresh_error,omitempty"`
}

type WalletInfo struct {
	OnHand int    `json:"on_hand"`
	Spent  uint64 `json:"spent"`
}

type RequestInfo struct {
	Total       int64 `json:"total"`
	TunnelFails int64 `json:"tunnel_fails"`
	Unpaid      int64 `json:"unpaid"`
	CrossOrigin int64 `json:"cross_origin_refused"`
}

// Status assembles the current state.
func (s *Server) Status() Status {
	st := Status{
		Endpoint: s.cfg.Addr,
		Upstream: s.cfg.Upstream,
		Paying:   "your own key",
		Retained: "nothing",
		Requests: RequestInfo{
			Total:       s.metrics.Requests.Load(),
			TunnelFails: s.metrics.TunnelFails.Load(),
			Unpaid:      s.metrics.NoToken.Load(),
			CrossOrigin: s.metrics.CrossOrigin.Load(),
		},
	}
	if s.listener != nil {
		st.Endpoint = s.listener.Addr().String()
	}
	if s.cfg.Tokens != nil {
		st.Paying = "tokens"
		if w, ok := s.cfg.Tokens.(WalletStatus); ok {
			st.Wallet = &WalletInfo{OnHand: w.Len(), Spent: w.Spent()}
		}
	}

	if s.cfg.Relays != nil {
		st.Directory = &DirectoryInfo{
			RelaysKnown: s.cfg.Relays.Len(),
			SignedBy:    s.cfg.Relays.SignedBy(),
		}
		if nick, addr, ok := s.cfg.Relays.Current(); ok {
			info := &RelayInfo{Nickname: nick, Address: addr, KeyMatched: true}
			if since, ok := s.cfg.Relays.GuardSince(); ok {
				info.SinceSeconds = int64(time.Since(since).Seconds())
			}
			st.Relay = info
		}
	} else if s.manualRelay != "" {
		// A manually pinned relay has no directory behind it, so there is
		// nothing to report but the address -- and saying so is better than
		// showing an empty panel that looks broken.
		st.Relay = &RelayInfo{Address: s.manualRelay, KeyMatched: true}
	}
	return st
}

// handleStatus serves the status document.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Never cached. A stale relay name shown as current would be a lie the
	// interface tells confidently.
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(s.Status())
}

// routes builds the handler: bearer's own paths, then the proxy for
// everything else.
func (s *Server) routes(proxy http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"status", s.handleStatus)
	mux.Handle("/", proxy)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkOrigin(r); err != nil {
			s.refuseOrigin(w, err)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
