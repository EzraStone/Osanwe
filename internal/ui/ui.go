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
	"connect-src 'self'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

type asset struct {
	body        []byte
	contentType string
}

var assetFiles = []struct {
	path        string
	file        string
	contentType string
}{
	{"/", "app.html", "text/html; charset=utf-8"},
	{"/assets/app.css", "assets/app.css", "text/css; charset=utf-8"},
	{"/assets/app.js", "assets/app.js", "text/javascript; charset=utf-8"},
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
		assets[spec.path] = asset{body: body, contentType: spec.contentType}
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

		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
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
