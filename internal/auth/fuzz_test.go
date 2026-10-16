package auth

import (
	"net/http"
	"testing"
)

// Every relay connection's credential goes through CheckHeader. For any
// secret a relay accepts at startup, the headers a client is told to send
// must authenticate, and an arbitrary header value must authenticate only if
// it actually carries that secret.
func FuzzCredentialCheckAcceptsExactlyTheSecret(f *testing.F) {
	f.Add("a-relay-secret-long-enough-0001", "Bearer a-relay-secret-long-enough-0001")
	f.Add("a-relay-secret-long-enough-0001", BasicHeader("a-relay-secret-long-enough-0001"))
	f.Add("a-relay-secret-long-enough-0001", "Basic !!!")
	f.Add("a-relay-secret-long-enough-0001", "Bearer")
	f.Add("a-relay-secret-long-enough-0001 ", "Bearer a-relay-secret-long-enough-0001")

	f.Fuzz(func(t *testing.T, secret, value string) {
		a, err := New(secret)
		if err != nil {
			return
		}
		for _, sent := range []string{Header(secret), BasicHeader(secret)} {
			if !a.CheckHeader(http.Header{"Proxy-Authorization": {sent}}) {
				t.Fatalf("relay started with secret %q rejects the header clients send: %q", secret, sent)
			}
		}
		got := a.CheckHeader(http.Header{"Proxy-Authorization": {value}})
		if want := extractSecret(value) == secret; got != want {
			t.Fatalf("secret %q, header %q: authenticated = %v, want %v", secret, value, got, want)
		}
	})
}
