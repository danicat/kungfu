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

package telemetry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danicat/kungfu/internal/state"
	"github.com/danicat/kungfu/internal/telemetry"
)

func TestBuildCommitPayload_ClampsAndFormats(t *testing.T) {
	deltas := []state.TelemetryDelta{
		{
			Skill:        "godoctor",
			Category:     "coding",
			Loads:        150, // Should clamp to MaxDeltaPerBatch (100)
			PageLoads:    2,
			SubpathLoads: 0,
			Runs:         5,
			Learns:       1,
		},
		{
			Skill:    "invalid-cat-skill",
			Category: "unknown-category",
			Loads:    5,
		},
		{
			Skill:    "bad/slash",
			Category: "coding",
			Loads:    5,
		},
	}

	req, applied := telemetry.BuildCommitPayload("danicat-dev", deltas)
	if len(applied) != 1 {
		t.Fatalf("expected 1 applied delta, got %d", len(applied))
	}
	if applied[0].Loads != telemetry.MaxDeltaPerBatch {
		t.Errorf("expected Loads clamped to %d, got %d", telemetry.MaxDeltaPerBatch, applied[0].Loads)
	}
	if len(req.Writes) != 1 {
		t.Fatalf("expected 1 write operation, got %d", len(req.Writes))
	}
	w := req.Writes[0]
	wantDoc := "projects/danicat-dev/databases/(default)/documents/skills/coding-godoctor"
	if w.Update.Name != wantDoc {
		t.Errorf("expected doc name %q, got %q", wantDoc, w.Update.Name)
	}
	// Expect 4 counter transforms (loads, page_loads, runs, learns) + 1 lastUpdated transform = 5
	if len(w.UpdateTransforms) != 5 {
		t.Errorf("expected 5 updateTransforms, got %d", len(w.UpdateTransforms))
	}
}

func TestClient_Flush(t *testing.T) {
	var gotCommit telemetry.CommitRequest
	var gotAuthHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/accounts:signUp":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"idToken":"test-jwt-token","expiresIn":"3600"}`))
		case "/v1/projects/danicat-dev/databases/(default)/documents:commit":
			gotAuthHeader = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&gotCommit); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"writeResults":[{"updateTime":"2026-10-14T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := &telemetry.Client{
		ProjectID:        "danicat-dev",
		APIKey:           "test-key",
		AuthBaseURL:      srv.URL,
		FirestoreBaseURL: srv.URL,
		Referer:          telemetry.DefaultReferer,
		HTTPClient:       srv.Client(),
	}

	deltas := []state.TelemetryDelta{
		{
			Skill:    "skill-analytics",
			Category: "analytics",
			Loads:    3,
			Runs:     2,
		},
	}

	applied, err := client.Flush(context.Background(), deltas)
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if len(applied) != 1 || applied[0].Loads != 3 || applied[0].Runs != 2 {
		t.Errorf("unexpected applied deltas: %+v", applied)
	}
	if gotAuthHeader != "Bearer test-jwt-token" {
		t.Errorf("expected Bearer test-jwt-token, got %q", gotAuthHeader)
	}
	if len(gotCommit.Writes) != 1 {
		t.Fatalf("expected 1 write in commit, got %d", len(gotCommit.Writes))
	}
}
