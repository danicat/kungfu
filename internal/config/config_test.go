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
	"testing"

	"github.com/danicat/kungfu/internal/config"
)

func TestConfig_Default(t *testing.T) {
	cfg, err := config.Load("/path/that/does/not/exist.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if !cfg.IsAutoUpdate() {
		t.Errorf("expected auto_update to default to true")
	}
	if u := cfg.GetCatalogURL("default"); u != config.DefaultCatalogURL {
		t.Errorf("expected default catalog URL %q, got %q", config.DefaultCatalogURL, u)
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
