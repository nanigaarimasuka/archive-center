package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

// The plugin keeps its whole settings object here as well, so settings that
// live only in the RisuAI plugin storage survive a new browser or device.
// The backend stores the object as is; the plugin owns its shape.

const pluginSettingsMaxBytes = 4 << 20

func pluginSettingsPath() (string, error) {
	path, err := multiAgentSettingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "plugin-settings.json"), nil
}

// LoadPersistedPluginSettings restores the plugin settings saved last.
func (s *Server) LoadPersistedPluginSettings() error {
	path, err := pluginSettingsPath()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isJSONObject(b) {
		return errPluginSettingsNotObject
	}
	s.pluginSettingsMu.Lock()
	s.pluginSettings = b
	s.pluginSettingsMu.Unlock()
	return nil
}

type pluginSettingsError string

func (e pluginSettingsError) Error() string { return string(e) }

const errPluginSettingsNotObject = pluginSettingsError("plugin_settings_not_object")

func isJSONObject(b []byte) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(b, &v) == nil && v != nil
}

func (s *Server) handlePluginSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPut {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginSettingsMaxBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "error": "plugin_settings_read_failed"})
			return
		}
		b = bytes.TrimSpace(b)
		if !isJSONObject(b) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "error": string(errPluginSettingsNotObject)})
			return
		}
		s.pluginSettingsMu.Lock()
		s.pluginSettings = b
		persisted := false
		if s.PersistRuntimeConfig {
			path, err := pluginSettingsPath()
			if err == nil {
				err = writeDataFileAtomic(path, b)
			}
			if err != nil {
				slog.Warn("plugin settings were kept in memory but not saved", "error", err)
			}
			persisted = err == nil
		}
		s.pluginSettingsMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ok",
			"persisted":   persisted,
			"persistence": runtimeConfigPersistence(s, persisted),
		})
		return
	}
	s.pluginSettingsMu.Lock()
	saved := s.pluginSettings
	s.pluginSettingsMu.Unlock()
	var settings json.RawMessage = []byte("null")
	if len(saved) > 0 {
		settings = saved
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "settings": settings})
}
