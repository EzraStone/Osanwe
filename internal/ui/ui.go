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

//go:embed app.html
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
	"style-src 'unsafe-inline'; " +
	"script-src 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

// Handler serves the interface rooted at prefix.
func Handler(prefix string) http.Handler {
	page, err := files.ReadFile("app.html")
	if err != nil {
		// Impossible unless the embed directive was removed, in which case
		// failing loudly beats serving an empty page.
		panic("ui: app.html is missing from the binary: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(prefix, "/"))
		if rest != "" && rest != "/" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		// The page reflects live local state, and a cached copy showing a relay
		// that is no longer in use would be a confident lie.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Write(page)
	})
}
