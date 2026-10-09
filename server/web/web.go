// Package web contains the ordinary, embedded revision-3 browser client.
package web

import (
	"embed"
	"net/http"
	"strings"
)

//go:generate go run gen.go
//go:embed dist/index.html dist/app.js dist/app.css
var assets embed.FS

// CSP forbids scripts, frames, remote resources and HTML injection. Element
// dimensions use CSSOM style properties (not author-supplied CSS); style-src
// uses CSSOM property assignment, permitted without inline style attributes.
const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; style-src-attr 'none'; connect-src 'self'; img-src blob: data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy := CSP
		// Some browsers don't include ws/wss in connect-src 'self'. Permit only
		// this exact Host, never a broad ws: scheme or reflected CSP syntax.
		if r.Host != "" && strings.IndexFunc(r.Host, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c))
		}) < 0 {
			policy = strings.Replace(policy, "connect-src 'self'", "connect-src 'self' ws://"+r.Host+" wss://"+r.Host, 1)
		}
		w.Header().Set("Content-Security-Policy", policy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name, typ := "", ""
		switch r.URL.Path {
		case "/":
			name, typ = "index.html", "text/html; charset=utf-8"
		case "/app.js":
			name, typ = "app.js", "text/javascript; charset=utf-8"
		case "/app.css":
			name, typ = "app.css", "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		b, err := assets.ReadFile("dist/" + name)
		if err != nil {
			http.Error(w, "assets unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", typ)
		if r.Method != "HEAD" {
			_, _ = w.Write(b)
		}
	})
}
