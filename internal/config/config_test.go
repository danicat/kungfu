// Copyright 2026 Daniela Petruzalek
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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/config"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-config-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp home: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", tempHome)
	_ = os.Setenv("USERPROFILE", tempHome)
	_ = os.Setenv("APPDATA", filepath.Join(tempHome, "AppData", "Roaming"))
	_ = os.Setenv("LOCALAPPDATA", filepath.Join(tempHome, "AppData", "Local"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tempHome, ".config"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(tempHome, ".cache"))
	_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
	_ = os.Setenv("KUNGFU_CONFIG", "")
	_ = os.Setenv("KUNGFU_TELEMETRY", "")

	code := m.Run()
	_ = os.RemoveAll(tempHome)
	os.Exit(code)
}

func TestConfig_Default(t *testing.T) {
	t.Setenv("KUNGFU_CONFIG_DIR", t.TempDir())
	t.Setenv("KUNGFU_TELEMETRY", "")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("expected no error for missing default file, got %v", err)
	}
	if cfg.IsTelemetryEnabled() {
		t.Errorf("expected local telemetry to default to false (opt-in)")
	}
	if u := cfg.GetCatalogURL("default"); u != config.DefaultCatalogURL {
		t.Errorf("expected default catalog URL %q, got %q", config.DefaultCatalogURL, u)
	}
	if u := cfg.GetCatalogURL("kungfu"); u != config.DefaultGitHubCatalog {
		t.Errorf("expected kungfu catalog URL %q, got %q", config.DefaultGitHubCatalog, u)
	}
	if _, err := config.Load("/path/that/does/not/exist.yaml"); err == nil {
		t.Errorf("expected error for missing explicit config path")
	}
}

func TestConfig_TelemetryFeatureFlag(t *testing.T) {
	tmpDir := t.TempDir()
	yamlFile := filepath.Join(tmpDir, "config.yaml")
	content := "telemetry:\n  enabled: true\n"
	if err := os.WriteFile(yamlFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write yaml file: %v", err)
	}

	t.Setenv("KUNGFU_TELEMETRY", "")
	cfg, err := config.Load(yamlFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.IsTelemetryEnabled() {
		t.Errorf("expected telemetry enabled from config.yaml")
	}

	// Env var overrides config.yaml
	t.Setenv("KUNGFU_TELEMETRY", "0")
	if cfg.IsTelemetryEnabled() {
		t.Errorf("expected KUNGFU_TELEMETRY=0 to disable telemetry")
	}

	defaultCfg := config.DefaultConfig()
	t.Setenv("KUNGFU_TELEMETRY", "1")
	if !defaultCfg.IsTelemetryEnabled() {
		t.Errorf("expected KUNGFU_TELEMETRY=1 to enable telemetry")
	}
}

func TestConfig_SaveDoesNotEmitTelemetryWhenDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("KUNGFU_CONFIG_DIR", tmpDir)
	cfg := config.DefaultConfig()
	savedPath, err := config.Save("", cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	raw, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(raw)
	if strings.Contains(content, "telemetry") || strings.Contains(content, "api_key") || strings.Contains(content, "project_id") {
		t.Errorf("saved default config should not contain telemetry, api_key, or project_id, got:\n%s", content)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(savedPath)
		if err != nil {
			t.Fatalf("Stat failed: %v", err)
		}
		if fi.Mode().Perm() != 0600 {
			t.Errorf("expected saved config permissions 0600, got %o", fi.Mode().Perm())
		}
	}
}

func TestConfig_LoadYAMLAndRejectUnknownFields(t *testing.T) {
	tmpDir := t.TempDir()
	yamlFile := filepath.Join(tmpDir, "config.yaml")
	content := `
telemetry:
  enabled: true
catalogs:
  default: "https://example.com/custom-catalog.json"
  team: "https://team.example.com/catalog.json"
`
	if err := os.WriteFile(yamlFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write yaml file: %v", err)
	}

	cfg, err := config.Load(yamlFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.IsTelemetryEnabled() {
		t.Errorf("expected telemetry enabled = true")
	}
	if u := cfg.GetCatalogURL("default"); u != "https://example.com/custom-catalog.json" {
		t.Errorf("expected custom default URL, got %q", u)
	}
	if u := cfg.GetCatalogURL("team"); u != "https://team.example.com/catalog.json" {
		t.Errorf("expected team URL, got %q", u)
	}

	// Unknown keys (such as legacy load.auto_update) load cleanly and report UnknownKeys warnings.
	legacyFile := filepath.Join(tmpDir, "legacy.yaml")
	if err := os.WriteFile(legacyFile, []byte("load:\n  auto_update: true\ntelemetry:\n  enabled: false\n  project_id: old\n"), 0600); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	legacyCfg, err := config.Load(legacyFile)
	if err != nil {
		t.Fatalf("expected legacy unknown keys to warn instead of failing, got: %v", err)
	}
	if len(legacyCfg.UnknownKeys) != 2 || legacyCfg.UnknownKeys[0] != "load" || legacyCfg.UnknownKeys[1] != "telemetry.project_id" {
		t.Errorf("UnknownKeys = %v, want [load telemetry.project_id]", legacyCfg.UnknownKeys)
	}

	// Malformed YAML syntax must still fail fast.
	badFile := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(badFile, []byte("catalogs: [unterminated\n"), 0600); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := config.Load(badFile); err == nil {
		t.Errorf("expected error for malformed YAML syntax, got nil")
	}
}

func TestConfig_ResolvePathAndXDG(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("KUNGFU_CONFIG_DIR", tmpDir)
	t.Setenv("KUNGFU_CONFIG", "")

	resolved, err := config.ResolveConfigPath("")
	if err != nil {
		t.Fatalf("ResolveConfigPath failed: %v", err)
	}
	if resolved != filepath.Join(tmpDir, "config.yaml") {
		t.Errorf("unexpected default resolved path: %s", resolved)
	}

	// XDG_CONFIG_HOME is respected when KUNGFU_CONFIG_DIR is empty
	xdgDir := t.TempDir()
	t.Setenv("KUNGFU_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	resolved, err = config.ResolveConfigPath("")
	if err != nil {
		t.Fatalf("ResolveConfigPath with XDG failed: %v", err)
	}
	if resolved != filepath.Join(xdgDir, "kungfu", "config.yaml") {
		t.Errorf("expected XDG path, got %s", resolved)
	}

	// KUNGFU_CONFIG overrides everything
	t.Setenv("KUNGFU_CONFIG", "/custom/env/path.yaml")
	resolved, err = config.ResolveConfigPath("")
	if err != nil {
		t.Fatalf("ResolveConfigPath with env failed: %v", err)
	}
	if resolved != "/custom/env/path.yaml" {
		t.Errorf("expected env override, got %s", resolved)
	}
}
