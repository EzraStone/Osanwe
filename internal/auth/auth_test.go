package auth

import (
	"net/http"
	"strings"
	"testing"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newT(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New(testSecret)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestNewRejectsShortSecrets(t *testing.T) {
	for _, s := range []string{"", "password", strings.Repeat("x", MinSecretLen-1)} {
		if _, err := New(s); err == nil {
			t.Errorf("New(%q) succeeded; secrets shorter than %d bytes must be refused", s, MinSecretLen)
		}
	}
	if _, err := New(strings.Repeat("x", MinSecretLen)); err != nil {
		t.Errorf("New rejected a secret of exactly MinSecretLen: %v", err)
	}
}

func TestCheck(t *testing.T) {
	a := newT(t)

	if !a.Check(testSecret) {
		t.Error("Check rejected the correct secret")
	}
	for _, bad := range []string{
		"",
		"wrong",
		testSecret + "x",               // extension
		testSecret[:len(testSecret)-1], // truncation
		strings.ToUpper(testSecret),    // case must matter
		" " + testSecret,               // no trimming of the credential itself
	} {
		if a.Check(bad) {
			t.Errorf("Check(%q) = true, want false", bad)
		}
	}
}

func TestNilAuthenticatorDeniesEverything(t *testing.T) {
	var a *Authenticator
	if a.Check(testSecret) {
		t.Error("nil *Authenticator accepted a secret; it must fail closed")
	}
	if a.CheckHeader(http.Header{"Proxy-Authorization": {Header(testSecret)}}) {
		t.Error("nil *Authenticator accepted a header; it must fail closed")
	}
}

func TestCheckHeader(t *testing.T) {
	a := newT(t)

	valid := []http.Header{
		{"Proxy-Authorization": {Header(testSecret)}},
		{"Authorization": {Header(testSecret)}},
		{"Proxy-Authorization": {BasicHeader(testSecret)}},
		{"Proxy-Authorization": {"bearer " + testSecret}}, // scheme is case-insensitive
		{"Proxy-Authorization": {"  Bearer   " + testSecret + "  "}},
		// A valid credential among several values must still be accepted.
		{"Proxy-Authorization": {"Bearer wrong", Header(testSecret)}},
	}
	for i, h := range valid {
		if !a.CheckHeader(h) {
			t.Errorf("valid header case %d rejected: %v", i, h)
		}
	}

	invalid := []http.Header{
		{},
		{"Proxy-Authorization": {""}},
		{"Proxy-Authorization": {"Bearer"}},
		{"Proxy-Authorization": {"Bearer "}},
		{"Proxy-Authorization": {"Bearer wrong"}},
		{"Proxy-Authorization": {testSecret}},             // no scheme
		{"Proxy-Authorization": {"Digest " + testSecret}}, // unsupported scheme
		{"Proxy-Authorization": {"Basic not-base64!!"}},
		{"Proxy-Authorization": {"Basic " + b64("no-colon-here")}},
		{"X-Api-Key": {testSecret}}, // wrong header entirely
	}
	for i, h := range invalid {
		if a.CheckHeader(h) {
			t.Errorf("invalid header case %d accepted: %v", i, h)
		}
	}
}

func TestBasicIgnoresUsername(t *testing.T) {
	a := newT(t)
	// Any username must work: there is no user model, and rejecting one
	// would only confuse clients that insist on supplying it.
	for _, user := range []string{"", "osanwe", "anything", "relay"} {
		h := http.Header{"Proxy-Authorization": {"Basic " + b64(user+":"+testSecret)}}
		if !a.CheckHeader(h) {
			t.Errorf("Basic credential with username %q was rejected", user)
		}
	}
	// A username that happens to equal the secret must not authenticate.
	h := http.Header{"Proxy-Authorization": {"Basic " + b64(testSecret+":wrong")}}
	if a.CheckHeader(h) {
		t.Error("Basic credential authenticated on the username rather than the password")
	}
}

func TestGenerateSecretIsUsableAndUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		s, err := GenerateSecret()
		if err != nil {
			t.Fatalf("GenerateSecret: %v", err)
		}
		if len(s) < MinSecretLen {
			t.Fatalf("GenerateSecret returned %d bytes, below MinSecretLen %d", len(s), MinSecretLen)
		}
		if seen[s] {
			t.Fatal("GenerateSecret returned a duplicate")
		}
		seen[s] = true

		a, err := New(s)
		if err != nil {
			t.Fatalf("New rejected a generated secret: %v", err)
		}
		if !a.Check(s) {
			t.Fatal("generated secret did not authenticate against itself")
		}
	}
}

func TestSecretIsNotRetainedInPlaintext(t *testing.T) {
	// The struct stores a digest, not the secret. This guards against a
	// refactor that "simplifies" the field back to a string, which would put
	// a working credential in any memory dump of a running relay.
	a := newT(t)
	if strings.Contains(string(a.digest[:]), testSecret) {
		t.Error("Authenticator retains the secret in plaintext")
	}
}

func b64(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out []byte
	data := []byte(s)
	for i := 0; i < len(data); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], data[i:])
		v := uint32(chunk[0])<<16 | uint32(chunk[1])<<8 | uint32(chunk[2])
		out = append(out, alphabet[v>>18&63], alphabet[v>>12&63])
		if n > 1 {
			out = append(out, alphabet[v>>6&63])
		} else {
			out = append(out, '=')
		}
		if n > 2 {
			out = append(out, alphabet[v&63])
		} else {
			out = append(out, '=')
		}
	}
	return string(out)
}
