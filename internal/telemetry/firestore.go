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

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/state"
	"github.com/danicat/kungfu/internal/version"
)

const (
	// DefaultFirestoreProjectID is the Firebase project ID for community telemetry.
	DefaultFirestoreProjectID = "danicat-dev"
	// DefaultFirebaseAPIKey is the public Firebase Web API key used for anonymous auth to danicat-dev Firestore.
	DefaultFirebaseAPIKey = "AIzaSyCazN8y12SLQdV5PPztJ7GsKnFrfCkXVvw" // gitleaks:allow
	// DefaultAuthBaseURL is the Firebase Identity Toolkit base URL for anonymous authentication.
	DefaultAuthBaseURL = "https://identitytoolkit.googleapis.com"
	// DefaultFirestoreBaseURL is the Cloud Firestore REST API base URL.
	DefaultFirestoreBaseURL = "https://firestore.googleapis.com"
	// DefaultReferer is the HTTP Referer header required by the restricted public Firebase Web API key.
	DefaultReferer = "https://skills.danicat.dev/"
	// MaxDeltaPerBatch caps each counter increment in a single flush to match Firestore security rule bounds.
	MaxDeltaPerBatch = state.MaxTelemetryDelta
	// MaxWritesPerCommit caps the number of document writes in a single Firestore commit batch.
	MaxWritesPerCommit = 500
)

var validCategories = map[string]bool{
	"agents":    true,
	"analytics": true,
	"coding":    true,
	"game-dev":  true,
	"media":     true,
	"writing":   true,
	"standards": true,
}

type StringValue struct {
	StringValue string `json:"stringValue"`
}

type IntegerValue struct {
	IntegerValue string `json:"integerValue"`
}

type DocumentUpdate struct {
	Name   string                 `json:"name"`
	Fields map[string]StringValue `json:"fields"`
}

type DocumentMask struct {
	FieldPaths []string `json:"fieldPaths"`
}

type FieldTransform struct {
	FieldPath        string        `json:"fieldPath"`
	Increment        *IntegerValue `json:"increment,omitempty"`
	SetToServerValue string        `json:"setToServerValue,omitempty"`
}

type WriteOperation struct {
	Update           DocumentUpdate   `json:"update"`
	UpdateMask       DocumentMask     `json:"updateMask"`
	UpdateTransforms []FieldTransform `json:"updateTransforms"`
}

// CommitRequest represents the JSON body sent to Firestore documents:commit.
type CommitRequest struct {
	Writes []WriteOperation `json:"writes"`
}

// Client flushes anonymous opt-in usage counter deltas to Cloud Firestore.
type Client struct {
	ProjectID        string
	APIKey           string
	AuthBaseURL      string
	FirestoreBaseURL string
	Referer          string
	HTTPClient       *http.Client
}

// NewClient creates a new Firestore telemetry client.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		ProjectID:        DefaultFirestoreProjectID,
		APIKey:           DefaultFirebaseAPIKey,
		AuthBaseURL:      DefaultAuthBaseURL,
		FirestoreBaseURL: DefaultFirestoreBaseURL,
		Referer:          DefaultReferer,
		HTTPClient:       httpClient,
	}
}

func clampDelta(v int) int {
	return min(MaxDeltaPerBatch, max(0, v))
}

func isValidSkillWrite(d state.TelemetryDelta) bool {
	if !validCategories[d.Category] {
		return false
	}
	if len(d.Skill) < 1 || len(d.Skill) > 64 || strings.Contains(d.Skill, "/") {
		return false
	}
	return true
}

// BuildCommitPayload constructs the Firestore documents:commit request and returns the clamped deltas that will be applied.
func BuildCommitPayload(projectID string, deltas []state.TelemetryDelta) (*CommitRequest, []state.TelemetryDelta) {
	if projectID == "" {
		projectID = DefaultFirestoreProjectID
	}

	var writes []WriteOperation
	var applied []state.TelemetryDelta

	for _, d := range deltas {
		if len(writes) >= MaxWritesPerCommit {
			break
		}
		if !isValidSkillWrite(d) {
			continue
		}

		clamped := state.TelemetryDelta{
			Skill:        d.Skill,
			Category:     d.Category,
			Loads:        clampDelta(d.Loads),
			PageLoads:    clampDelta(d.PageLoads),
			SubpathLoads: clampDelta(d.SubpathLoads),
			Runs:         clampDelta(d.Runs),
			Learns:       clampDelta(d.Learns),
		}
		if clamped.Loads == 0 && clamped.PageLoads == 0 && clamped.SubpathLoads == 0 && clamped.Runs == 0 && clamped.Learns == 0 {
			continue
		}

		counters := []struct {
			field string
			val   int
		}{
			{"loads", clamped.Loads},
			{"page_loads", clamped.PageLoads},
			{"subpath_loads", clamped.SubpathLoads},
			{"runs", clamped.Runs},
			{"learns", clamped.Learns},
		}

		var transforms []FieldTransform
		for _, c := range counters {
			if c.val > 0 {
				transforms = append(transforms, FieldTransform{
					FieldPath: c.field,
					Increment: &IntegerValue{IntegerValue: strconv.Itoa(c.val)},
				})
			}
		}
		transforms = append(transforms, FieldTransform{
			FieldPath:        "lastUpdated",
			SetToServerValue: "REQUEST_TIME",
		})

		docName := fmt.Sprintf("projects/%s/databases/(default)/documents/skills/%s-%s", projectID, d.Category, d.Skill)
		writes = append(writes, WriteOperation{
			Update: DocumentUpdate{
				Name: docName,
				Fields: map[string]StringValue{
					"category": {StringValue: d.Category},
					"skill":    {StringValue: d.Skill},
				},
			},
			UpdateMask:       DocumentMask{FieldPaths: []string{"category", "skill"}},
			UpdateTransforms: transforms,
		})
		applied = append(applied, clamped)
	}

	return &CommitRequest{Writes: writes}, applied
}

// Flush authenticates anonymously and commits the clamped counter deltas to Cloud Firestore.
func (c *Client) Flush(ctx context.Context, deltas []state.TelemetryDelta) ([]state.TelemetryDelta, error) {
	reqPayload, applied := BuildCommitPayload(c.ProjectID, deltas)
	if len(applied) == 0 {
		return nil, nil
	}

	idToken, err := c.signUpAnonymous(ctx)
	if err != nil {
		return nil, err
	}

	commitURL := fmt.Sprintf("%s/v1/projects/%s/databases/(default)/documents:commit",
		strings.TrimRight(c.FirestoreBaseURL, "/"),
		url.PathEscape(c.ProjectID),
	)

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("telemetry: encoding commit payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, commitURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("telemetry: creating Firestore commit request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+idToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	if c.Referer != "" {
		req.Header.Set("Referer", c.Referer)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telemetry: Firestore commit failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("telemetry: Firestore commit returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	return applied, nil
}

func (c *Client) signUpAnonymous(ctx context.Context) (string, error) {
	authURL := fmt.Sprintf("%s/v1/accounts:signUp?key=%s",
		strings.TrimRight(c.AuthBaseURL, "/"),
		url.QueryEscape(c.APIKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader([]byte(`{"returnSecureToken":true}`)))
	if err != nil {
		return "", fmt.Errorf("telemetry: creating anonymous auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	if c.Referer != "" {
		req.Header.Set("Referer", c.Referer)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("telemetry: Firebase anonymous auth request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("telemetry: Firebase anonymous auth returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var authResp struct {
		IDToken string `json:"idToken"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&authResp); err != nil {
		return "", fmt.Errorf("telemetry: decoding Firebase anonymous auth response: %w", err)
	}
	if authResp.IDToken == "" {
		return "", fmt.Errorf("telemetry: Firebase anonymous auth response missing idToken")
	}

	return authResp.IDToken, nil
}
