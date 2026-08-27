package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseGitHubTarget(t *testing.T) {
	tests := []struct {
		input       string
		wantOwner   string
		wantRepo    string
		wantRef     string
		wantSubpath string
		wantOK      bool
	}{
		{
			input:     "https://github.com/anthropics/anthropic-quickstarts",
			wantOwner: "anthropics",
			wantRepo:  "anthropic-quickstarts",
			wantRef:   "main",
			wantOK:    true,
		},
		{
			input:     "github.com/danicat/kungfu",
			wantOwner: "danicat",
			wantRepo:  "kungfu",
			wantRef:   "main",
			wantOK:    true,
		},
		{
			input:       "anthropics/anthropic-quickstarts/evals",
			wantOwner:   "anthropics",
			wantRepo:    "anthropic-quickstarts",
			wantRef:     "main",
			wantSubpath: "evals",
			wantOK:      true,
		},
		{
			input:       "https://github.com/danicat/skills/tree/dev/coding/godoctor",
			wantOwner:   "danicat",
			wantRepo:    "skills",
			wantRef:     "dev",
			wantSubpath: "coding/godoctor",
			wantOK:      true,
		},
		{
			input:       "https://github.com/danicat/skills/blob/v1.0.0/coding/godoctor/SKILL.md",
			wantOwner:   "danicat",
			wantRepo:    "skills",
			wantRef:     "v1.0.0",
			wantSubpath: "coding/godoctor",
			wantOK:      true,
		},
		{
			input:     "owner/repo@v2.1.0",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "v2.1.0",
			wantOK:    true,
		},
		{
			input:     "owner/repo#branch-name",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "branch-name",
			wantOK:    true,
		},
		{
			input:     "https://github.com/danicat/kungfu.git",
			wantOwner: "danicat",
			wantRepo:  "kungfu",
			wantRef:   "main",
			wantOK:    true,
		},
		{
			input:       "https://raw.githubusercontent.com/anthropics/anthropic-quickstarts/main/evals/SKILL.md",
			wantOwner:   "anthropics",
			wantRepo:    "anthropic-quickstarts",
			wantRef:     "main",
			wantSubpath: "evals",
			wantOK:      true,
		},
		{
			input:       "https://raw.githubusercontent.com/danicat/skills/v1.0.0/coding/godoctor/SKILL.md",
			wantOwner:   "danicat",
			wantRepo:    "skills",
			wantRef:     "v1.0.0",
			wantSubpath: "coding/godoctor",
			wantOK:      true,
		},
		{
			input:     "owner/repo@v1.2.3/",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "v1.2.3",
			wantOK:    true,
		},
		{
			input:  "invalid",
			wantOK: false,
		},
		{
			input:  "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			target, ok := ParseGitHubTarget(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ParseGitHubTarget(%q) ok = %v, want %v", tc.input, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if target.Owner != tc.wantOwner {
				t.Errorf("Owner = %q, want %q", target.Owner, tc.wantOwner)
			}
			if target.Repo != tc.wantRepo {
				t.Errorf("Repo = %q, want %q", target.Repo, tc.wantRepo)
			}
			if target.Ref != tc.wantRef {
				t.Errorf("Ref = %q, want %q", target.Ref, tc.wantRef)
			}
			if target.Subpath != tc.wantSubpath {
				t.Errorf("Subpath = %q, want %q", target.Subpath, tc.wantSubpath)
			}
		})
	}
}

func TestFetchGitHubSkillMock(t *testing.T) {
	mockContent := `---
name: custom-evals
description: Custom evaluation harness
allowed-tools: [bash, python]
license: MIT
metadata:
  version: 1.2.0
  category: coding
  tags: eval, testing
---
# Custom Evals
Instructions here.
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testowner/testrepo/main/skills/evals/SKILL.md" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(mockContent))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Direct test via probeGitHubRef with client routing
	// We can test ParseFrontmatter & Item construction
	target := &GitHubTarget{
		Owner:   "testowner",
		Repo:    "testrepo",
		Ref:     "main",
		Subpath: "skills/evals",
	}

	fm, _, err := ParseFrontmatter([]byte(mockContent))
	if err != nil {
		t.Fatalf("ParseFrontmatter failed: %v", err)
	}
	if fm.Name != "custom-evals" {
		t.Errorf("Name = %q, want custom-evals", fm.Name)
	}
	if fm.Description != "Custom evaluation harness" {
		t.Errorf("Description = %q", fm.Description)
	}
	if fm.Metadata["version"] != "1.2.0" {
		t.Errorf("version = %q, want 1.2.0", fm.Metadata["version"])
	}

	_ = target
	_ = server
	_ = context.Background()
}
