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

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-main-test-home-*")
	if err == nil {
		_ = os.Setenv("HOME", tempHome)
		_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
		_ = os.Setenv("KUNGFU_STATE_PATH", filepath.Join(tempHome, ".config", "kungfu", "state.json"))
		_ = os.Setenv("KUNGFU_CONFIG", "")
		_ = os.Setenv("KUNGFU_TELEMETRY", "0")
		_ = os.Setenv("DO_NOT_TRACK", "")
	}
	code := m.Run()
	if tempHome != "" {
		_ = os.RemoveAll(tempHome)
	}
	os.Exit(code)
}

func TestRun_CLI_Basics(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"version long", []string{"--version"}, ExitSuccess, "kungfu version"},
		{"version short", []string{"-V"}, ExitSuccess, "kungfu version"},
		{"help long", []string{"--help"}, ExitSuccess, "USAGE:"},
		{"help short", []string{"-h"}, ExitSuccess, "USAGE:"},
		{"help command", []string{"help"}, ExitSuccess, "USAGE:"},
		{"no args", []string{}, ExitUsage, "USAGE:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, tt.wantSub) {
				t.Errorf("Run(%v) output %q does not contain %q", tt.args, out, tt.wantSub)
			}
		})
	}
}

func TestRun_CLI_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"nonexistentcmd"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("Run(nonexistentcmd) = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("expected unknown command error, got: %s", stderr.String())
	}
}

func TestRun_CLI_ListAndShow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.0","items":[]}`))
	}))
	defer srv.Close()
	t.Setenv("KUNGFU_URL", srv.URL)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"list"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(list) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "CATEGORY") {
		t.Errorf("expected category listing in stdout: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"show", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(show kungfu) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "SKILL: kungfu") {
		t.Errorf("expected kungfu in show output: %s", stdout.String())
	}
}

func TestRun_CLI_Load(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.0","items":[]}`))
	}))
	defer srv.Close()
	t.Setenv("KUNGFU_URL", srv.URL)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"load", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(load kungfu) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# KungFu: Agent Skills Gateway") {
		t.Errorf("expected markdown content: %s", stdout.String())
	}
}
