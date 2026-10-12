package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestIdentityHostsAddsAConcreteBindHostOnce(t *testing.T) {
	loopback := []string{"localhost", "127.0.0.1", "::1"}
	tests := []struct {
		addr string
		want []string
	}{
		{"0.0.0.0:8443", loopback},
		{"[::]:8443", loopback},
		{":8443", loopback},
		{"not an address", loopback},
		{"127.0.0.1:8443", loopback},
		{"[::1]:8443", loopback},
		{"localhost:8443", loopback},
		{"relay.example:8443", append(append([]string{}, loopback...), "relay.example")},
		{"203.0.113.7:8443", append(append([]string{}, loopback...), "203.0.113.7")},
	}
	for _, test := range tests {
		if got := identityHosts(test.addr); !reflect.DeepEqual(got, test.want) {
			t.Errorf("identityHosts(%q) = %q, want %q", test.addr, got, test.want)
		}
	}
}

func TestDisplayAddrNamesWildcardBinds(t *testing.T) {
	tests := map[string]string{
		"0.0.0.0:8443":       "<this-host>:8443",
		"[::]:8443":          "<this-host>:8443",
		":8443":              "<this-host>:8443",
		"203.0.113.7:8443":   "203.0.113.7:8443",
		"relay.example:8443": "relay.example:8443",
		"not an address":     "not an address",
	}
	for addr, want := range tests {
		if got := displayAddr(addr); got != want {
			t.Errorf("displayAddr(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestPublishSendsTheSignedDescriptorVerbatim(t *testing.T) {
	descriptor := []byte("osanwe-descriptor v1\nidentity abc\nsignature xyz\n")
	var gotBody []byte
	var gotType, gotAgent, gotMethod string
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotType, gotAgent = r.Method, r.Header.Get("Content-Type"), r.Header.Get("User-Agent")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer authority.Close()

	if err := publish(authority.URL, descriptor); err != nil {
		t.Fatalf("publish = %v, want nil on 202", err)
	}
	if gotMethod != http.MethodPost || gotType != "text/plain" || gotAgent != "osanwe-ranger" {
		t.Errorf("request = %s, Content-Type %q, User-Agent %q", gotMethod, gotType, gotAgent)
	}
	if string(gotBody) != string(descriptor) {
		t.Errorf("authority received %q, want the descriptor unchanged", gotBody)
	}
}

func TestPublishExplainsEachAuthorityRefusal(t *testing.T) {
	tests := []struct {
		status  int
		wantNil bool
		want    string
	}{
		{http.StatusOK, true, ""},
		{http.StatusAccepted, true, ""},
		{http.StatusForbidden, false, "add this relay's identity to their accept list"},
		{http.StatusConflict, false, "already holds a newer descriptor"},
		{http.StatusServiceUnavailable, false, "does not accept submissions"},
		{http.StatusInternalServerError, false, "HTTP 500"},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "  authority says why  ", test.status)
			}))
			defer authority.Close()
			err := publish(authority.URL, []byte("descriptor"))
			if test.wantNil {
				if err != nil {
					t.Fatalf("publish = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("publish = %v, want an error containing %q", err, test.want)
			}
			if !strings.HasSuffix(err.Error(), ": authority says why") {
				t.Errorf("error %q does not end with the authority's trimmed explanation", err)
			}
		})
	}
}

func TestPublishBoundsTheAuthorityExplanation(t *testing.T) {
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, strings.Repeat("x", 64<<10))
	}))
	defer authority.Close()
	err := publish(authority.URL, []byte("descriptor"))
	if err == nil {
		t.Fatal("publish succeeded on 403")
	}
	if n := strings.Count(err.Error(), "x"); n != 8<<10 {
		t.Fatalf("error carried %d bytes of the authority's body, want the 8 KiB cap", n)
	}
}

func TestPublishReportsAnUnreachableAuthority(t *testing.T) {
	authority := httptest.NewServer(http.NotFoundHandler())
	url := authority.URL
	authority.Close()
	if err := publish(url, []byte("descriptor")); err == nil {
		t.Fatal("publish to a closed authority returned nil")
	}
}
