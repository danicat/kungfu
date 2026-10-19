// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/config"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-config-test-*")
	if err == nil {
		_ = os.Setenv("HOME", tempHome)
		_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
		_ = os.Setenv("KUNGFU_CONFIG", "")
		_ = os.Setenv("KUNGFU_TELEMETRY", "")
		_ = os.Setenv("DO_NOT_TRACK", "")
	}
	code := m.Run()
	if tempHome != "" {
		_ = os.RemoveAll(tempHome)
	}
	os.Exit(code)
}

func TestConfig_Default(t *testing.T) {
	t.Setenv("KUNGFU_CONFIG_DIR", t.TempDir())
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("expected no error for missing default file, got %v", err)
	}
	if !cfg.IsAutoUpdate() {
		t.Errorf("expected auto_update to default to true")
	}
	if u := cfg.GetCatalogURL("default"); u != config.DefaultCatalogURL {
		t.Errorf("expected default catalog URL %q, got %q", config.DefaultCatalogURL, u)
	}
	if _, err := config.Load("/path/that/does/not/exist.yaml"); err == nil {
		t.Errorf("expected error for missing explicit config path")
	}
}

func TestConfig_SaveDoesNotLeakKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("KUNGFU_CONFIG_DIR", tmpDir)
	cfg := config.DefaultConfig()
	cfg.SetTelemetryEnabled(true)
	savedPath, err := config.Save("", cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	raw, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if strings.Contains(string(raw), "api_key") || strings.Contains(string(raw), "project_id") {
		t.Errorf("saved config should not contain api_key or project_id, got:\n%s", string(raw))
	}
}

func TestConfig_LoadYAML(t *testing.T) {
	tmpDir := t.TempDir()
	yamlFile := filepath.Join(tmpDir, "config.yaml")
	content := `
load:
  auto_update: false
catalogs:
  default: "https://example.com/custom-catalog.json"
  team: "https://team.internal/catalog.json"
`
	if err := os.WriteFile(yamlFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write yaml file: %v", err)
	}

	cfg, err := config.Load(yamlFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.IsAutoUpdate() {
		t.Errorf("expected auto_update = false")
	}
	if u := cfg.GetCatalogURL("default"); u != "https://example.com/custom-catalog.json" {
		t.Errorf("expected custom default URL, got %q", u)
	}
	if u := cfg.GetCatalogURL("team"); u != "https://team.internal/catalog.json" {
		t.Errorf("expected team URL, got %q", u)
	}
}

func TestConfig_LoadJSON(t *testing.T) {
	tmpDir := t.TempDir()
	jsonFile := filepath.Join(tmpDir, "config.json")
	content := `{
  "load": {
    "auto_update": false
  },
  "catalogs": {
    "default": "https://json.example.com/catalog.json"
  }
}`
	if err := os.WriteFile(jsonFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write json file: %v", err)
	}

	cfg, err := config.Load(jsonFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.IsAutoUpdate() {
		t.Errorf("expected auto_update = false")
	}
	if u := cfg.GetCatalogURL("default"); u != "https://json.example.com/catalog.json" {
		t.Errorf("expected custom JSON URL, got %q", u)
	}
}

func TestConfig_ResolvePath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("KUNGFU_CONFIG_DIR", tmpDir)

	// No file exists -> returns default path
	resolved := config.ResolveConfigPath("")
	if resolved != filepath.Join(tmpDir, "config.yaml") {
		t.Errorf("unexpected default resolved path: %s", resolved)
	}

	// config.json exists -> resolves to json
	jsonPath := filepath.Join(tmpDir, "config.json")
	_ = os.WriteFile(jsonPath, []byte("{}"), 0644)
	resolved = config.ResolveConfigPath("")
	if resolved != jsonPath {
		t.Errorf("expected to resolve to existing config.json, got %s", resolved)
	}

	// config.yaml exists -> takes precedence over json
	yamlPath := filepath.Join(tmpDir, "config.yaml")
	_ = os.WriteFile(yamlPath, []byte("{}"), 0644)
	resolved = config.ResolveConfigPath("")
	if resolved != yamlPath {
		t.Errorf("expected to resolve to existing config.yaml, got %s", resolved)
	}

	// KUNGFU_CONFIG overrides
	t.Setenv("KUNGFU_CONFIG", "/custom/env/path.yaml")
	if r := config.ResolveConfigPath(""); r != "/custom/env/path.yaml" {
		t.Errorf("expected env override, got %s", r)
	}
}
