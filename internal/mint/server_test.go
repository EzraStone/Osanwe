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
