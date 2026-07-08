package middleware

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
)

// Authenticate wraps next with an X-API-Key check against keys. When keys
// is empty, authentication is a no-op — main.go is responsible for logging
// that loudly at startup, since a silently open API is exactly the kind of
// failure that should be visible, not swallowed here.
func Authenticate(keys map[string]struct{}) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		if len(keys) == 0 {
			return next
		}
		return func(w http.ResponseWriter, r *http.Request) {
			if key := r.Header.Get("X-API-Key"); key != "" && validKey(keys, key) {
				next(w, r)
				return
			}
			writeUnauthorized(w)
		}
	}
}

// validKey checks membership in constant time so response latency can't
// be used to guess how close a candidate key is to a valid one.
func validKey(keys map[string]struct{}, key string) bool {
	for k := range keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	if _, err := w.Write([]byte(`{"error":"invalid or missing API key"}`)); err != nil {
		slog.Default().Error("write auth error response", "error", err)
	}
}
