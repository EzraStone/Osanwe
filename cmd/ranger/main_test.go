package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/auth"
	"github.com/EzraStone/osanwe/internal/directory"
	"github.com/EzraStone/osanwe/internal/policy"
	"github.com/EzraStone/osanwe/internal/ranger"
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

	if _, err := publish(authority.URL, descriptor); err != nil {
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
			outcome, err := publish(authority.URL, []byte("descriptor"))
			if test.wantNil {
				if err != nil || outcome != "authority says why" {
					t.Fatalf("publish = %q, %v; want the authority's trimmed message and nil", outcome, err)
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
	_, err := publish(authority.URL, []byte("descriptor"))
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
	if _, err := publish(url, []byte("descriptor")); err == nil {
		t.Fatal("publish to a closed authority returned nil")
	}
}

func newMetricsTestRelay(t *testing.T) *ranger.Server {
	t.Helper()
	allow, err := policy.Parse([]string{"api.anthropic.com:443"})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.New(strings.Repeat("s", auth.MinSecretLen))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ranger.New(ranger.Config{
		Addr: "127.0.0.1:0", TLS: &tls.Config{}, Allowlist: allow, Auth: authenticator, MaxTunnels: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestMetricsAreWellFormedPrometheusText(t *testing.T) {
	srv := newMetricsTestRelay(t)
	srv.Metrics().Accepted.Add(3)
	srv.Metrics().BytesToClient.Add(4096)

	rec := httptest.NewRecorder()
	metricsHandler(srv).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type %q", ct)
	}

	help, types, values := map[string]bool{}, map[string]string{}, map[string]string{}
	scanner := bufio.NewScanner(rec.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		switch {
		case len(fields) >= 3 && fields[0] == "#" && fields[1] == "HELP":
			help[fields[2]] = true
		case len(fields) == 4 && fields[0] == "#" && fields[1] == "TYPE":
			types[fields[2]] = fields[3]
		case len(fields) == 2:
			values[fields[0]] = fields[1]
		default:
			t.Errorf("unparseable line %q", scanner.Text())
		}
	}
	if len(values) == 0 {
		t.Fatal("no samples")
	}
	for name := range values {
		if !help[name] {
			t.Errorf("%s has no HELP line", name)
		}
		want := "gauge"
		if strings.HasSuffix(name, "_total") {
			want = "counter"
		}
		if types[name] != want {
			t.Errorf("%s TYPE = %q, want %q", name, types[name], want)
		}
	}
	for name, want := range map[string]string{
		"osanwe_ranger_accepted_total":        "3",
		"osanwe_ranger_bytes_to_client_total": "4096",
		"osanwe_ranger_tunnels_reserved":      "0",
		"osanwe_ranger_tunnels_limit":         "7",
	} {
		if values[name] != want {
			t.Errorf("%s = %q, want %q", name, values[name], want)
		}
	}
}

func TestHealthzFailsOnceTheRelayStops(t *testing.T) {
	srv := newMetricsTestRelay(t)
	handler := metricsHandler(srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthy relay: %d %q", rec.Code, rec.Body.String())
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped relay: status %d, want 503 so a load balancer drains it", rec.Code)
	}
}

func noEnv(string) string { return "" }

// runRanger runs the relay command in dir and fails the test if it starts
// serving: every case that uses it must refuse or exit by itself.
func runRanger(t *testing.T, dir string, args []string, getenv func(string) string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	full := append([]string{"-dir", dir, "-addr", "127.0.0.1:0", "-metrics", ""}, args...)
	done := make(chan error, 1)
	go func() { done <- run(full, getenv, &out) }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(30 * time.Second):
		t.Fatalf("ranger %q started serving instead of exiting", args)
		return "", nil
	}
}

func TestRunRefusesAnOpenOrUnboundedRelay(t *testing.T) {
	goodSecret := strings.Repeat("s", auth.MinSecretLen)
	secret := func(value string) func(string) string {
		return func(name string) string {
			if name == "OSANWE_RANGER_SECRET" {
				return value
			}
			return ""
		}
	}
	tests := []struct {
		name   string
		args   []string
		getenv func(string) string
		want   string
	}{
		{"no secret", []string{"-allow", "api.anthropic.com"}, noEnv, "no secret set"},
		{"short secret", []string{"-allow", "api.anthropic.com"}, secret("short"), "need at least"},
		{"short secret on the command line", []string{"-allow", "api.anthropic.com", "-secret", "short"}, secret(goodSecret), "need at least"},
		{"no destinations", nil, secret(goodSecret), "no -allow destinations given"},
		{"unusable destination", []string{"-allow", "https://api.anthropic.com/v1"}, secret(goodSecret), "is a URL; give only the host"},
		{"no tunnel capacity", []string{"-allow", "api.anthropic.com", "-max-tunnels", "0"}, secret(goodSecret), "-max-tunnels must be at least 1"},
		{"descriptor without destinations", []string{"-descriptor", "relay.desc", "-nickname", "r1"}, noEnv, "-descriptor needs -allow"},
		{"descriptor without nickname", []string{"-descriptor", "relay.desc", "-allow", "api.anthropic.com"}, noEnv, "-descriptor needs -nickname"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runRanger(t, t.TempDir(), test.args, test.getenv)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

func TestRunWritesADescriptorClientsCanVerify(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "relay.desc")

	pin, err := runRanger(t, dir, []string{"-pin"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runRanger(t, dir, []string{"-identity"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runRanger(t, dir, []string{
		"-descriptor", out, "-nickname", "elrond", "-contact", "ops@relay.example",
		"-allow", "api.anthropic.com", "-advertise", "relay.example:8443", "-descriptor-validity", "6h",
	}, noEnv); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	d, err := directory.ParseDescriptor(raw)
	if err != nil {
		t.Fatalf("descriptor does not verify: %v", err)
	}
	if d.Nickname != "elrond" || d.Address != "relay.example:8443" || d.Contact != "ops@relay.example" {
		t.Errorf("descriptor = %q at %q contact %q", d.Nickname, d.Address, d.Contact)
	}
	if d.TLSPin != strings.TrimSpace(pin) {
		t.Errorf("descriptor pin %q, but -pin printed %q", d.TLSPin, pin)
	}
	if d.Identity != strings.TrimSpace(identity) {
		t.Errorf("descriptor identity %q, but -identity printed %q", d.Identity, identity)
	}
	if !reflect.DeepEqual(d.Destinations, []string{"api.anthropic.com:443"}) {
		t.Errorf("destinations = %q", d.Destinations)
	}
	if got := d.Expires.Sub(d.Published); got != 6*time.Hour {
		t.Errorf("validity = %s, want 6h", got)
	}
}

func TestRunKeepsItsIdentityAcrossInvocations(t *testing.T) {
	dir := t.TempDir()
	for _, flag := range []string{"-pin", "-identity"} {
		first, err := runRanger(t, dir, []string{flag}, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		second, err := runRanger(t, dir, []string{flag}, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" || first != second {
			t.Errorf("%s printed %q then %q; a relay's identity must not change between runs", flag, first, second)
		}
	}
}

func TestRunWillNotAdvertiseAWildcardAddress(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "relay.desc")
	_, err := runRanger(t, dir, []string{
		"-addr", "0.0.0.0:8443", "-descriptor", out, "-nickname", "elrond", "-allow", "api.anthropic.com",
	}, noEnv)
	if err == nil || !strings.Contains(err.Error(), "-advertise is required") {
		t.Fatalf("run error = %v, want the -advertise refusal", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an undialable descriptor was written anyway (stat error %v)", err)
	}
}

func TestRunGeneratesUsableDistinctSecrets(t *testing.T) {
	first, err := runRanger(t, t.TempDir(), []string{"-gen-secret"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runRanger(t, t.TempDir(), []string{"-gen-secret"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	first, second = strings.TrimSpace(first), strings.TrimSpace(second)
	if _, err := auth.New(first); err != nil {
		t.Fatalf("-gen-secret printed a secret ranger itself rejects: %v", err)
	}
	if first == second {
		t.Fatal("-gen-secret printed the same secret twice")
	}
}

func TestRunPrintsItsVersion(t *testing.T) {
	out, err := runRanger(t, t.TempDir(), []string{"-version"}, noEnv)
	if err != nil || !strings.HasPrefix(out, "ranger") {
		t.Fatalf("-version printed %q, %v", out, err)
	}
}

func TestPublishFallsBackToAcceptedForAnEmptyReply(t *testing.T) {
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer authority.Close()
	if outcome, err := publish(authority.URL, []byte("descriptor")); err != nil || outcome != "accepted" {
		t.Fatalf("publish = %q, %v; want \"accepted\"", outcome, err)
	}
}
