package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
)

func TestRuntimeConfigSurvivesARestartWhenPersisted(t *testing.T) {
	t.Setenv("ARCHIVE_CENTER_DATA_DIR", t.TempDir())
	sync := func(s *Server, body map[string]any) map[string]any {
		mux := http.NewServeMux()
		s.RegisterRoutes(mux)
		encoded, _ := json.Marshal(body)
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/config/update", bytes.NewReader(encoded)))
		var result map[string]any
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &result) != nil {
			t.Fatal(r.Code, r.Body)
		}
		return result
	}

	// Without persistence nothing is written.
	if result := sync(NewServer(config.Default()), map[string]any{"mainModel": "unsaved"}); result["persisted"] != false || result["persistence"] != "runtime_only" {
		t.Fatal(result)
	}
	path, _ := runtimeConfigPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("runtime config written without persistence: %v", err)
	}

	first := NewServer(config.Default())
	first.PersistRuntimeConfig = true
	if result := sync(first, map[string]any{"mainProvider": "risu", "mainModel": "pluginmodel:::A", "mainTemperature": 0.4, "mainTimeout": 90, "llmRetryCount": 3}); result["persisted"] != true {
		t.Fatal(result)
	}
	// A later sync changes only the keys it carries.
	sync(first, map[string]any{"mainModel": "pluginmodel:::B", "criticReprocessingIntervalSec": 99999})

	restarted := NewServer(config.Default())
	if err := restarted.LoadPersistedRuntimeConfig(); err != nil {
		t.Fatal(err)
	}
	want, got := first.runtimeConfigSnapshot(), restarted.runtimeConfigSnapshot()
	if !got.Synced || got.MainModel != "pluginmodel:::B" || got.MainProvider != "risu" || *got.MainTemperature != 0.4 || got.CriticReprocessingIntervalSec != 3600 {
		t.Fatalf("restored %+v", got)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("restored %+v, want %+v", got, want)
	}
	// Windows reports no Unix permission bits.
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("saved file %v %v", info, err)
	}
}
