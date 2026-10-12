package httpapi

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// Synced settings are saved next to the preprocessing settings, so a restarted
// backend keeps them without waiting for the plugin to sync again.

func runtimeConfigPath() (string, error) {
	path, err := multiAgentSettingsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "runtime-config.json"), nil
}

// LoadPersistedRuntimeConfig restores the settings saved by the last sync.
func (s *Server) LoadPersistedRuntimeConfig() error {
	path, err := runtimeConfigPath()
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
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		return err
	}
	s.runtimeConfigPersistMu.Lock()
	defer s.runtimeConfigPersistMu.Unlock()
	s.updateRuntimeConfig(body)
	return nil
}

// updateAndPersistRuntimeConfig applies body and, when persistence is on,
// saves the resulting settings; it reports whether they were saved.
func (s *Server) updateAndPersistRuntimeConfig(body map[string]any) ([]string, bool) {
	if !s.PersistRuntimeConfig {
		return s.updateRuntimeConfig(body), false
	}
	s.runtimeConfigPersistMu.Lock()
	defer s.runtimeConfigPersistMu.Unlock()
	updated := s.updateRuntimeConfig(body)
	s.RuntimeConfigMu.RLock()
	b, err := json.Marshal(s.runtimeConfigSaved)
	s.RuntimeConfigMu.RUnlock()
	if err == nil {
		err = writeRuntimeConfigFile(b)
	}
	if err != nil {
		slog.Warn("runtime config was applied but not saved", "error", err)
		return updated, false
	}
	return updated, true
}

func writeRuntimeConfigFile(b []byte) error {
	path, err := runtimeConfigPath()
	if err != nil {
		return err
	}
	return writeDataFileAtomic(path, b)
}

// writeDataFileAtomic replaces path with b so readers never see a partial file.
func writeDataFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

func runtimeConfigPersistence(s *Server, persisted bool) string {
	switch {
	case persisted:
		return "data_dir"
	case s.PersistRuntimeConfig:
		return "runtime_only_save_failed"
	default:
		return "runtime_only"
	}
}
