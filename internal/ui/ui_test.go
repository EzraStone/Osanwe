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
	for _, want := range []string{"<title>Osanwë</title>", "assets/app.css", "assets/app.js", `id="stop"`, `aria-label="Stop generating"`, `id="storageWarning"`, `role="alert"`} {
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

func TestThePageContainsNoInlineCode(t *testing.T) {
	page, err := files.ReadFile("app.html")
	if err != nil {
		t.Fatalf("app.html is not embedded: %v", err)
	}
	text := string(page)
	for _, forbidden := range []string{"<style", " style=", "<script>"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the embedded page still contains inline code marker %q", forbidden)
		}
	}
}

func TestTheScriptHasNoHTMLOrCookieStorageSink(t *testing.T) {
	script, err := files.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("assets/app.js is not embedded: %v", err)
	}
	text := string(script)
	for _, forbidden := range []string{".innerHTML", ".outerHTML", "insertAdjacentHTML", "document.cookie"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the token-spending interface contains the DOM/storage sink %q", forbidden)
		}
	}
	for _, required := range []string{"osanwe-theme", "osanwe-model", "osanwe-retention"} {
		if !strings.Contains(text, required) {
			t.Fatalf("the documented localStorage setting %q is missing", required)
		}
	}
	for _, privateKey := range []string{"osanwe-conversation", "osanwe-message", "osanwe-prompt"} {
		if strings.Contains(text, "localStorage.setItem(\""+privateKey) {
			t.Fatalf("conversation content key %q was put in localStorage", privateKey)
		}
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
	if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Opener-Policy = %q", got)
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q", got)
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

func TestOnlyKnownAssetsAreServed(t *testing.T) {
	h := Handler("/_osanwe/")
	tests := []struct {
		path        string
		contentType string
	}{
		{"/_osanwe/", "text/html; charset=utf-8"},
		{"/_osanwe/assets/app.css", "text/css; charset=utf-8"},
		{"/_osanwe/assets/app.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/api.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/conversation.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/disclosure.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/lifecycle.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/models.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/snippets.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/sse.js", "text/javascript; charset=utf-8"},
		{"/_osanwe/assets/storage.js", "text/javascript; charset=utf-8"},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", tc.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != tc.contentType {
			t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", tc.path, got)
		}
	}

	for _, path := range []string{
		"/_osanwe/assets/", "/_osanwe/assets/missing.js", "/_osanwe/assets/../app.html",
		"/_osanwe/assets/%2e%2e/app.html", "/_osanwe/assets/app.js.map",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s served an unintended asset", path)
		}
	}
}
