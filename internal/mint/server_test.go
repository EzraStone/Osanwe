package mint

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMintResponsesAreNeverCacheable(t *testing.T) {
	server := NewServer(newMint(t), quietLog()).Handler()
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/key", nil),
		httptest.NewRequest(http.MethodPost, "/issue", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/missing", nil),
	}
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s %s Cache-Control = %q", request.Method, request.URL.Path, got)
		}
		if got := recorder.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s %s Referrer-Policy = %q", request.Method, request.URL.Path, got)
		}
		if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s %s X-Content-Type-Options = %q", request.Method, request.URL.Path, got)
		}
		if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
			t.Errorf("%s %s set %d cookies", request.Method, request.URL.Path, len(cookies))
		}
	}
}

func TestIssueAcceptsOnlyTheDocumentedJSONShape(t *testing.T) {
	server := NewServer(newMint(t), quietLog()).Handler()
	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
	}{
		{"missing content type", "", `{"blinded":"AA"}`, http.StatusUnsupportedMediaType},
		{"wrong content type", "text/plain", `{"blinded":"AA"}`, http.StatusUnsupportedMediaType},
		{"buyer identity field", "application/json", `{"blinded":"AA","email":"person@example.test"}`, http.StatusBadRequest},
		{"duplicate receipt", "application/json", `{"receipt":"one","receipt":"two","blinded":"AA"}`, http.StatusBadRequest},
		{"duplicate blinded message", "application/json", `{"blinded":"AA","blinded":"AQ"}`, http.StatusBadRequest},
		{"non-text receipt", "application/json", `{"receipt":{"buyer":"person"},"blinded":"AA"}`, http.StatusBadRequest},
		{"trailing document", "application/json", `{"blinded":"AA"} {"blinded":"AA"}`, http.StatusBadRequest},
		{"trailing scalar", "application/json", `{"blinded":"AA"} true`, http.StatusBadRequest},
		{"documented shape reaches protocol validation", "application/json; charset=utf-8", `{"receipt":"paid","blinded":"AA"}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/issue", strings.NewReader(tc.body))
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d; body %s", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}
}
