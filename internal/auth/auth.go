// Package auth authenticates clients to a ranger.
//
// Phase 2 uses a shared secret, deliberately. It is not the end state -- the
// design calls for blind-signed tokens in Phase 3, so that a relay learns
// nothing about who is connecting beyond the fact that someone paid. A shared
// secret is what an operator can actually deploy today, and it exists to stop
// a stranger using the relay, not to identify the user to the operator.
//
// The relay must therefore never log or store the presented secret. Doing so
// would turn an access-control mechanism into a per-user identifier and give a
// seized relay something worth having.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// MinSecretLen is the shortest secret a ranger will start with. Twenty-four
// bytes of hex is the documented recipe; this floor rejects the operator who
// types "password" into the config and assumes it is fine.
const MinSecretLen = 24

// scheme is the Proxy-Authorization scheme. Bearer rather than Basic: there is
// no username here, and Basic would invite operators to put one in.
const scheme = "Bearer"

// Authenticator checks the Proxy-Authorization header against a shared secret.
//
// The secret is stored hashed so a memory dump of a running relay does not
// hand over a working credential, and comparison is constant time so response
// timing does not leak how much of a guess was correct.
type Authenticator struct {
	digest [sha256.Size]byte
}

// New returns an Authenticator for the given secret.
func New(secret string) (*Authenticator, error) {
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("auth: secret is %d bytes, need at least %d; generate one with `openssl rand -hex 24`",
			len(secret), MinSecretLen)
	}
	return &Authenticator{digest: sha256.Sum256([]byte(secret))}, nil
}

// GenerateSecret returns a fresh random secret suitable for New.
func GenerateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Check reports whether the presented secret is correct.
//
// Both the hashing and the comparison run unconditionally, including when the
// input is empty, so that "no credential" and "wrong credential" take the same
// time. An early return on the empty case would let an attacker distinguish
// the two, which is a small leak but a free one to avoid.
func (a *Authenticator) Check(presented string) bool {
	if a == nil {
		return false
	}
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(got[:], a.digest[:]) == 1
}

// CheckHeader reports whether an HTTP header carries a valid credential. It
// accepts either Proxy-Authorization (correct for a proxy) or Authorization
// (which some clients send instead), and tolerates the older Basic form so
// that curl and browser proxy settings keep working.
func (a *Authenticator) CheckHeader(h http.Header) bool {
	if a == nil {
		return false
	}
	// Evaluate every candidate rather than returning on the first match, so
	// the work done does not depend on which header carried the credential.
	ok := false
	for _, name := range []string{"Proxy-Authorization", "Authorization"} {
		for _, raw := range h.Values(name) {
			if a.Check(extractSecret(raw)) {
				ok = true
			}
		}
	}
	return ok
}

// extractSecret pulls the credential out of an authorization header value.
// Basic credentials are accepted in "user:secret" form, where the username is
// ignored -- there is no user model here, and rejecting a non-empty username
// would only produce confusing failures for clients that insist on one.
func extractSecret(raw string) string {
	raw = strings.TrimSpace(raw)
	kind, rest, found := strings.Cut(raw, " ")
	if !found {
		return ""
	}
	rest = strings.TrimSpace(rest)

	switch {
	case strings.EqualFold(kind, scheme):
		return rest
	case strings.EqualFold(kind, "Basic"):
		decoded, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return ""
		}
		_, secret, ok := strings.Cut(string(decoded), ":")
		if !ok {
			return ""
		}
		return secret
	default:
		return ""
	}
}

// Header returns the header value a client should send.
func Header(secret string) string { return scheme + " " + secret }

// BasicHeader returns a Basic-form value, for clients that only support
// proxy credentials as a URL userinfo component.
func BasicHeader(secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("osanwe:"+secret))
}
