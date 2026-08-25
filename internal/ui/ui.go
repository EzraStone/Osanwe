// Package ui serves the local interface.
//
// The page is compiled into the binary rather than read from disk. One file to
// run means no install step, nothing to keep in sync with the daemon, and no
// directory an attacker can drop a replacement page into -- a page served from
// this origin can talk to the client, so where it comes from is a security
// question and not a packaging preference.
package ui

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed app.html assets/*
var files embed.FS

// contentSecurityPolicy is deliberately close to nothing.
//
// The page is entirely self-contained, so it needs no outside origin at all,
// and saying so turns a whole class of problems into a browser-enforced
// refusal: a tampered page, an injected script or a well-meant analytics
// snippet cannot reach anywhere. connect-src 'self' is the load-bearing
// clause, because it means nothing on this page can send a prompt anywhere but
// back to this client.
const contentSecurityPolicy = "default-src 'none'; " +
	"style-src 'self'; " +
	"script-src 'self'; " +
	"img-src 'self' data:; " +
	// The two text faces are served from this binary like every other asset.
	// 'self' rather than a font CDN keeps the promise the rest of this policy
	// makes: the page reaches no outside origin, so no outside origin learns
	// that somebody opened it.
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"frame-src 'self'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

// The runner is always embedded without allow-same-origin. Standalone
// JavaScript runs in a disposable blob worker, while generated HTML runs in a
// second opaque-origin data frame. Both are recreated per run and neither may
// connect to the client or an outside origin. This narrower document policy
// permits those two containers without weakening the credential-bearing page.
const runnerContentSecurityPolicy = "default-src 'none'; " +
	"style-src 'unsafe-inline'; " +
	"script-src 'unsafe-inline' 'unsafe-eval'; " +
	"worker-src blob:; " +
	"img-src data: blob:; " +
	"connect-src 'none'; " +
	"webrtc 'block'; " +
	"font-src 'none'; " +
	"media-src 'none'; " +
	"object-src 'none'; " +
	"frame-src data:; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'self'"

// The Connection Allowlist blocks every outside endpoint, including channels
// such as DNS prefetch and WebRTC that CSP does not cover consistently. CSP
// still blocks the response origin itself. Local data/about documents inherit
// both policies, which lets the runner create its disposable preview frame.
const runnerConnectionAllowlist = "(response-origin);webrtc=block"

type asset struct {
	body        []byte
	contentType string
	csp         string
}

var assetFiles = []struct {
	path        string
	file        string
	contentType string
	csp         string
}{
	{"/", "app.html", "text/html; charset=utf-8", ""},
	{"/assets/app.css", "assets/app.css", "text/css; charset=utf-8", ""},
	{"/assets/app.js", "assets/app.js", "text/javascript; charset=utf-8", ""},
	{"/assets/api.js", "assets/api.js", "text/javascript; charset=utf-8", ""},
	{"/assets/code.js", "assets/code.js", "text/javascript; charset=utf-8", ""},
	{"/assets/conversation.js", "assets/conversation.js", "text/javascript; charset=utf-8", ""},
	{"/assets/cormorant-garamond.woff2", "assets/cormorant-garamond.woff2", "font/woff2", ""},
	{"/assets/disclosure.js", "assets/disclosure.js", "text/javascript; charset=utf-8", ""},
	{"/assets/lifecycle.js", "assets/lifecycle.js", "text/javascript; charset=utf-8", ""},
	{"/assets/lora.woff2", "assets/lora.woff2", "font/woff2", ""},
	{"/assets/models.js", "assets/models.js", "text/javascript; charset=utf-8", ""},
	{"/assets/runner.html", "assets/runner.html", "text/html; charset=utf-8", runnerContentSecurityPolicy},
	{"/assets/snippets.js", "assets/snippets.js", "text/javascript; charset=utf-8", ""},
	{"/assets/sse.js", "assets/sse.js", "text/javascript; charset=utf-8", ""},
	{"/assets/storage.js", "assets/storage.js", "text/javascript; charset=utf-8", ""},
}

// Handler serves the interface rooted at prefix.
func Handler(prefix string) http.Handler {
	assets := make(map[string]asset, len(assetFiles))
	for _, spec := range assetFiles {
		body, err := files.ReadFile(spec.file)
		if err != nil {
			// Impossible unless the embed directive or manifest was changed, in
			// which case failing loudly beats serving a partial application.
			panic("ui: " + spec.file + " is missing from the binary: " + err.Error())
		}
		assets[spec.path] = asset{body: body, contentType: spec.contentType, csp: spec.csp}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(prefix, "/"))
		if rest == "" {
			rest = "/"
		}
		asset, ok := assets[rest]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", asset.contentType)

		csp := asset.csp
		if csp == "" {
			csp = contentSecurityPolicy
		}
		w.Header().Set("Content-Security-Policy", csp)
		if rest == "/assets/runner.html" {
			w.Header().Set("Connection-Allowlist", runnerConnectionAllowlist)
		}
		// The page reflects live local state, and a cached copy showing a relay
		// that is no longer in use would be a confident lie.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "accelerometer=(), autoplay=(), camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if r.Method != http.MethodHead {
			_, _ = w.Write(asset.body)
		}
	})
}
