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

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultCatalogURL is the default remote catalog URL.
const DefaultCatalogURL = "https://skills.danicat.dev/.well-known/agent-skills/index.json"

// DefaultGitHubCatalog is the default GitHub catalog repository.
const DefaultGitHubCatalog = "danicat/kungfu"

const maxConfigBytes = 1 << 20 // 1 MiB

// TelemetryConfig controls opt-in local usage history recording.
type TelemetryConfig struct {
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// Config represents the user-level configuration (~/.config/kungfu/config.yaml).
type Config struct {
	Telemetry   *TelemetryConfig  `json:"telemetry,omitempty" yaml:"telemetry,omitempty"`
	Catalogs    map[string]string `json:"catalogs,omitempty" yaml:"catalogs,omitempty"`
	UnknownKeys []string          `json:"-" yaml:"-"`
}

// DefaultConfig returns the default configuration with built-in catalogs.
func DefaultConfig() *Config {
	return &Config{
		Catalogs: map[string]string{
			"default": DefaultCatalogURL,
			"kungfu":  DefaultGitHubCatalog,
		},
	}
}

// IsTelemetryEnabled reports whether opt-in local usage history recording is enabled.
// Precedence: KUNGFU_TELEMETRY env var overrides telemetry.enabled in config.yaml.
// Defaults to false (opt-in) so normal read commands perform zero state writes.
func (c *Config) IsTelemetryEnabled() bool {
	if raw := strings.TrimSpace(os.Getenv("KUNGFU_TELEMETRY")); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		default:
			if v, err := strconv.ParseBool(raw); err == nil {
				return v
			}
		}
	}
	return c != nil && c.Telemetry != nil && c.Telemetry.Enabled
}

// IsBuiltinCatalog reports whether name is one of the always built-in catalogs.
func IsBuiltinCatalog(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "default", "kungfu":
		return true
	default:
		return false
	}
}

// GetCatalogURL returns the source URL/target for a catalog name, or empty string if not found.
func (c *Config) GetCatalogURL(name string) string {
	if c != nil && c.Catalogs != nil {
		if u, ok := c.Catalogs[name]; ok && u != "" {
			return u
		}
	}
	switch name {
	case "default":
		return DefaultCatalogURL
	case "kungfu":
		return DefaultGitHubCatalog
	default:
		return ""
	}
}

// UserConfigDir resolves the user configuration directory.
// Precedence: KUNGFU_CONFIG_DIR -> XDG_CONFIG_HOME/kungfu -> OS default (~/.config/kungfu on Unix, %APPDATA%/kungfu on Windows).
func UserConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("KUNGFU_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "kungfu"), nil
	}
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil || dir == "" {
			return "", fmt.Errorf("config: unable to resolve user config directory: %w", err)
		}
		return filepath.Join(dir, "kungfu"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("config: unable to resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kungfu"), nil
}

// ResolveConfigPath locates the active YAML configuration file.
// Precedence:
// 1. Explicit customPath (e.g. from --config flag)
// 2. KUNGFU_CONFIG env var
// 3. <UserConfigDir>/config.yaml
func ResolveConfigPath(customPath string) (string, error) {
	if customPath != "" {
		return customPath, nil
	}
	if envPath := strings.TrimSpace(os.Getenv("KUNGFU_CONFIG")); envPath != "" {
		return envPath, nil
	}
	dir, err := UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads and strictly parses user configuration from the resolved YAML path.
// If no explicit customPath or KUNGFU_CONFIG is set and config.yaml does not exist, DefaultConfig is returned.
func Load(customPath string) (*Config, error) {
	explicit := customPath != "" || strings.TrimSpace(os.Getenv("KUNGFU_CONFIG")) != ""
	path, err := ResolveConfigPath(customPath)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	_ = f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, readErr)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("config: %s exceeds maximum size (%d bytes)", path, maxConfigBytes)
	}

	cfg := DefaultConfig()
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("config: parsing YAML %s: %w", path, err)
		}
		var rawMap map[string]any
		if err := yaml.Unmarshal(data, &rawMap); err == nil {
			for k, v := range rawMap {
				switch k {
				case "catalogs":
				case "telemetry":
					if tm, ok := v.(map[string]any); ok {
						for tk := range tm {
							if tk != "enabled" {
								cfg.UnknownKeys = append(cfg.UnknownKeys, "telemetry."+tk)
							}
						}
					}
				default:
					cfg.UnknownKeys = append(cfg.UnknownKeys, k)
				}
			}
			sort.Strings(cfg.UnknownKeys)
		}
	}

	if cfg.Catalogs == nil {
		cfg.Catalogs = make(map[string]string)
	}
	if cfg.Catalogs["default"] == "" {
		cfg.Catalogs["default"] = DefaultCatalogURL
	}
	if cfg.Catalogs["kungfu"] == "" {
		cfg.Catalogs["kungfu"] = DefaultGitHubCatalog
	}

	return cfg, nil
}

// Save writes the given configuration atomically as YAML to the resolved config path and returns the path written.
func Save(customPath string, cfg *Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config: cannot save nil config")
	}
	path, err := ResolveConfigPath(customPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("config: creating directory %s: %w", dir, err)
	}

	// Copy to avoid mutating caller and omit disabled telemetry block cleanly.
	toSave := *cfg
	if toSave.Telemetry != nil && !toSave.Telemetry.Enabled {
		toSave.Telemetry = nil
	}

	data, err := yaml.Marshal(&toSave)
	if err != nil {
		return "", fmt.Errorf("config: encoding YAML: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return "", fmt.Errorf("config: creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmpFile.Write(data); err != nil {
		cleanup()
		return "", fmt.Errorf("config: writing temp file %s: %w", tmpPath, err)
	}
	if err := tmpFile.Chmod(0600); err != nil {
		cleanup()
		return "", fmt.Errorf("config: setting permissions on %s: %w", tmpPath, err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("config: syncing temp file %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("config: closing temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("config: renaming %s -> %s: %w", tmpPath, path, err)
	}
	return path, nil
}
