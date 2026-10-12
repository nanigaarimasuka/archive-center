package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
)

func TestClientDebugRequested(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		want  bool
	}{
		{set: false, want: true},
		{value: "1", set: true, want: true},
		{value: "0", set: true, want: false},
		{value: " 0 ", set: true, want: false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/health", nil)
		if tc.set {
			r.Header.Set(clientDebugHeader, tc.value)
		}
		if got := clientDebugRequested(r); got != tc.want {
			t.Fatalf("header %q (set=%v): got %v, want %v", tc.value, tc.set, got, tc.want)
		}
	}
}

func TestCORSAllowsClientDebugHeader(t *testing.T) {
	srv := NewServer(config.Default())
	handler := srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodOptions, "/prepare-turn", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Access-Control-Request-Headers", "content-type,x-archive-center-debug")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	allowed := w.Header().Get("Access-Control-Allow-Headers")
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Skip("origin not allowed by default config")
	}
	if !strings.Contains(strings.ToLower(allowed), strings.ToLower(clientDebugHeader)) {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %s", allowed, clientDebugHeader)
	}
}
