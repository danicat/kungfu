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

package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCatalog(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, ".kungfu-state.json"))

	// Create mock HTTP server for custom catalog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"name": "mycatalog",
			"skills": [
				{
					"name": "remoteskill",
					"description": "A remote test skill",
					"category": "remote",
					"url": "https://example.com/skill/SKILL.md"
				}
			]
		}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr, testEmbeddedFS)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. Add catalog
	stdout.Reset()
	stderr.Reset()
	code := app.RunCatalog([]string{"add", server.URL, "--name", "mock-cat"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog add = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Added catalog \"mock-cat\"") {
		t.Errorf("expected added catalog confirmation, got: %s", stdout.String())
	}

	// 2. List catalogs
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"list"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog list = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "mock-cat") {
		t.Errorf("expected mock-cat in catalog list, got: %s", stdout.String())
	}

	// 3. Sync catalog
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"sync", "mock-cat"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog sync = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Synchronized") {
		t.Errorf("expected synchronized confirmation, got: %s", stdout.String())
	}

	// 4. Remove catalog
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"remove", "mock-cat"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog remove = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Removed catalog \"mock-cat\"") {
		t.Errorf("expected removed confirmation, got: %s", stdout.String())
	}

	// 5. Attempt to remove default catalog should fail
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"remove", "default"})
	if code == ExitSuccess || strings.Contains(stdout.String(), "Removed catalog \"default\"") {
		t.Errorf("expected default catalog removal to be prevented")
	}

	// 6. Sync default catalog
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"sync", "default"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog sync default = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Synchronized") {
		t.Errorf("expected synchronized confirmation for default, got: %s", stdout.String())
	}
}
