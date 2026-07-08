package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticate_NoKeysConfiguredAllowsAll(t *testing.T) {
	called := false
	h := Authenticate(nil)(func(w http.ResponseWriter, r *http.Request) { called = true })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/users", nil))

	if !called {
		t.Fatal("expected next to be called when no API keys are configured")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthenticate_RejectsMissingOrWrongKey(t *testing.T) {
	keys := map[string]struct{}{"secret-key": {}}
	called := false
	h := Authenticate(keys)(func(w http.ResponseWriter, r *http.Request) { called = true })

	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"wrong key", "wrong-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/users", nil)
			if tt.header != "" {
				req.Header.Set("X-API-Key", tt.header)
			}
			rec := httptest.NewRecorder()
			h(rec, req)

			if called {
				t.Fatal("next must not be called for an invalid key")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAuthenticate_AcceptsValidKey(t *testing.T) {
	keys := map[string]struct{}{"secret-key": {}}
	called := false
	h := Authenticate(keys)(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set("X-API-Key", "secret-key")
	rec := httptest.NewRecorder()
	h(rec, req)

	if !called {
		t.Fatal("expected next to be called with a valid key")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
}
