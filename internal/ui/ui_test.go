package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The page is compiled in, so a build that lost it must fail loudly rather
// than serve an empty window.
func TestThePageIsInTheBinary(t *testing.T) {
	page, err := files.ReadFile("app.html")
	if err != nil {
		t.Fatalf("app.html is not embedded: %v", err)
	}
	for _, want := range []string{"<title>Osanwë</title>", "assets/app.css", "assets/app.js"} {
		if !strings.Contains(string(page), want) {
			t.Fatalf("the embedded page is missing %q", want)
		}
	}

	script, err := files.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("assets/app.js is not embedded: %v", err)
	}
	for _, want := range []string{"/_osanwe/", "status", "/v1/messages"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("the embedded script is missing %q", want)
		}
	}

	if _, err := files.ReadFile("assets/app.css"); err != nil {
		t.Fatalf("assets/app.css is not embedded: %v", err)
	}
}

// The policy is what makes a tampered or injected script harmless: it cannot
// reach any origin but this client, so a prompt cannot be sent anywhere else.
func TestThePolicyPinsThePageToThisClient(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler("/_osanwe/").ServeHTTP(rec, httptest.NewRequest("GET", "/_osanwe/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "style-src 'self'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("policy %q is missing %q", csp, want)
		}
	}
	// A page that could be framed by another site could be clickjacked into
	// spending tokens.
	if strings.Contains(csp, "connect-src *") || strings.Contains(csp, "https:") {
		t.Fatalf("policy %q allows an outside origin", csp)
	}
	if strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("policy %q permits inline code", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	permissions := rec.Header().Get("Permissions-Policy")
	for _, denied := range []string{"camera=()", "geolocation=()", "microphone=()", "payment=()"} {
		if !strings.Contains(permissions, denied) {
			t.Errorf("Permissions-Policy %q does not deny %s", permissions, denied)
		}
	}
}

// The handler owns exactly one path. Anything else under the prefix is a
// mistake, and answering it with the page would hide that.
func TestOnlyTheRootIsServed(t *testing.T) {
	h := Handler("/_osanwe/")
	for _, path := range []string{"/_osanwe/", "/_osanwe"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d, want 200", path, rec.Code)
		}
	}
	for _, path := range []string{"/_osanwe/app.html", "/_osanwe/../secret", "/_osanwe/anything"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("%s was served the page; the handler should own only its root", path)
		}
	}
}

func TestTheInterfaceIsReadOnlyHTTP(t *testing.T) {
	h := Handler("/_osanwe/")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/_osanwe/", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s Allow = %q, want GET, HEAD", method, got)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/_osanwe/assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d body bytes", rec.Body.Len())
	}
}
