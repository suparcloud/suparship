package server

import "net/http"

// legacyServiceRoute wraps an http.HandlerFunc to add deprecation signal
// headers on every response.
//
// The headers follow RFC 8594 (the "Deprecation" HTTP header field):
//
//   - Deprecation: true — signals that this endpoint is deprecated.
//   - Link: <...>; rel="deprecation" — points to the migration guide (if
//     served at a known URL); omitted in this MVP because the docs are not
//     served over HTTP.
//
// These headers are non-breaking: existing clients that do not inspect them
// continue to function normally. API clients that do inspect them can surface
// a warning to their operators.
//
// All legacy service-oriented HTTP routes in rbac.go are wrapped with this
// helper. See docs/migration-app-model.md for the full transition guide.
func legacyServiceRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		next(w, r)
	}
}
