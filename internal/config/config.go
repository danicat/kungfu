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

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultCatalogURL is the default remote catalog URL.
const DefaultCatalogURL = "https://skills.danicat.dev/catalog.json"

// LoadConfig holds configuration options for kungfu load behavior.
type LoadConfig struct {
	AutoUpdate *bool `json:"auto_update,omitempty" yaml:"auto_update,omitempty"`
}

// Config represents the user-level configuration (~/.config/kungfu/config.yaml or config.json).
type Config struct {
	Load     LoadConfig        `json:"load,omitempty" yaml:"load,omitempty"`
	Catalogs map[string]string `json:"catalogs,omitempty" yaml:"catalogs,omitempty"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	autoUpdate := true
	return &Config{
		Load: LoadConfig{
			AutoUpdate: &autoUpdate,
		},
		Catalogs: map[string]string{
			"default": DefaultCatalogURL,
		},
	}
}

// IsAutoUpdate returns true if auto-update is enabled (default true).
func (c *Config) IsAutoUpdate() bool {
	if c == nil || c.Load.AutoUpdate == nil {
		return true
	}
	return *c.Load.AutoUpdate
}

// GetCatalogURL returns the URL for a catalog name, or empty string if not found.
func (c *Config) GetCatalogURL(name string) string {
	if c == nil || c.Catalogs == nil {
		if name == "default" {
			return DefaultCatalogURL
		}
		return ""
	}
	if u, ok := c.Catalogs[name]; ok && u != "" {
		return u
	}
	if name == "default" {
		return DefaultCatalogURL
	}
	return ""
}

// UserConfigDir returns ~/.config/kungfu, or respects KUNGFU_CONFIG_DIR.
func UserConfigDir() string {
	if dir := os.Getenv("KUNGFU_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "kungfu")
}

// ResolveConfigPath locates the active configuration file.
// Precedence:
// 1. Explicit path parameter (e.g. from --config flag)
// 2. KUNGFU_CONFIG env var
// 3. ~/.config/kungfu/config.yaml
// 4. ~/.config/kungfu/config.yml
// 5. ~/.config/kungfu/config.json
func ResolveConfigPath(customPath string) string {
	if customPath != "" {
		return customPath
	}
	if envPath := os.Getenv("KUNGFU_CONFIG"); envPath != "" {
		return envPath
	}

	dir := UserConfigDir()
	candidates := []string{
		filepath.Join(dir, "config.yaml"),
		filepath.Join(dir, "config.yml"),
		filepath.Join(dir, "config.json"),
	}

	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}

	return filepath.Join(dir, "config.yaml")
}

// Load reads and parses user configuration from the resolved path.
// If the file does not exist, DefaultConfig is returned without error.
func Load(customPath string) (*Config, error) {
	path := ResolveConfigPath(customPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}

	cfg := DefaultConfig()

	// Try YAML first (YAML is a superset of JSON, but yaml.v3 handles both cleanly)
	if err := yaml.Unmarshal(data, cfg); err != nil {
		// Fallback to JSON unmarshaler
		if jerr := json.Unmarshal(data, cfg); jerr != nil {
			return nil, fmt.Errorf("config: parsing %s: %w", path, err)
		}
	}

	if cfg.Catalogs == nil {
		cfg.Catalogs = make(map[string]string)
	}
	if _, ok := cfg.Catalogs["default"]; !ok || cfg.Catalogs["default"] == "" {
		cfg.Catalogs["default"] = DefaultCatalogURL
	}

	return cfg, nil
}
