package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The other update tests exercise the upstream update itself.
func init() { inAppUpdateBlockedForFork = false }

// On the fork, check, download and apply refuse without reaching upstream;
// status still answers.
func TestInAppUpdateRefusesOnTheFork(t *testing.T) {
	inAppUpdateBlockedForFork = true
	defer func() { inAppUpdateBlockedForFork = false }()
	srv := &Server{}
	srv.Cfg.UpdateEnabled = true
	mux := http.NewServeMux()
	srv.registerUpdateRoutes(mux)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/update/check", nil),
		httptest.NewRequest(http.MethodPost, "/update/download", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/update/apply", strings.NewReader(`{}`)),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "update_unavailable_on_fork") || !strings.Contains(rec.Body.String(), "fork") {
			t.Fatalf("%s %s = %d %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}
}
