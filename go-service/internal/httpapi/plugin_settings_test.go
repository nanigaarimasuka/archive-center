package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
)

func TestPluginSettingsAreSharedAcrossDevicesAndRestarts(t *testing.T) {
	t.Setenv("ARCHIVE_CENTER_DATA_DIR", t.TempDir())
	call := func(s *Server, method, body string) (int, map[string]any) {
		mux := http.NewServeMux()
		s.RegisterRoutes(mux)
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest(method, "/config/plugin-settings", strings.NewReader(body)))
		var result map[string]any
		_ = json.Unmarshal(r.Body.Bytes(), &result)
		return r.Code, result
	}

	first := NewServer(config.Default())
	first.PersistRuntimeConfig = true
	if code, result := call(first, http.MethodGet, ""); code != 200 || result["settings"] != nil {
		t.Fatal(code, result)
	}
	for _, bad := range []string{"", "[]", "null", "{"} {
		if code, _ := call(first, http.MethodPut, bad); code != 400 {
			t.Fatalf("%q accepted with %d", bad, code)
		}
	}
	saved := `{"maxInjectionChars":12000,"memoryDeliveryBudgets":{"event_recent":900},"settingsSavedAt":5}`
	if code, result := call(first, http.MethodPut, saved); code != 200 || result["persisted"] != true {
		t.Fatal(code, result)
	}

	// Another device reads the same object; a restarted backend still has it.
	restarted := NewServer(config.Default())
	if err := restarted.LoadPersistedPluginSettings(); err != nil {
		t.Fatal(err)
	}
	code, result := call(restarted, http.MethodGet, "")
	settings, _ := result["settings"].(map[string]any)
	budgets, _ := settings["memoryDeliveryBudgets"].(map[string]any)
	if code != 200 || settings["maxInjectionChars"] != 12000.0 || budgets["event_recent"] != 900.0 {
		t.Fatal(code, result)
	}

	// Without persistence the settings are kept for this process only.
	memoryOnly := NewServer(config.Default())
	if code, result := call(memoryOnly, http.MethodPut, `{"topK":3}`); code != 200 || result["persisted"] != false {
		t.Fatal(code, result)
	}
	path, _ := pluginSettingsPath()
	if b, err := os.ReadFile(path); err != nil || string(b) != saved {
		t.Fatalf("saved file changed: %s %v", b, err)
	}
}
