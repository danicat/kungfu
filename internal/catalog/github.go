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

package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/version"
)

// GitHubTarget captures parsed repository and subpath components.
type GitHubTarget struct {
	Owner       string // e.g. "anthropics"
	Repo        string // e.g. "anthropic-quickstarts"
	Ref         string // e.g. "main", "v1.0.0", "master"
	Subpath     string // e.g. "skills/evals", "evals", ""
	OriginalURL string // e.g. "https://github.com/anthropics/anthropic-quickstarts"
}

// RepoString returns "owner/repo".
func (t *GitHubTarget) RepoString() string {
	return fmt.Sprintf("%s/%s", t.Owner, t.Repo)
}

// FullRepoURL returns "https://github.com/owner/repo".
func (t *GitHubTarget) FullRepoURL() string {
	return fmt.Sprintf("https://github.com/%s/%s", t.Owner, t.Repo)
}

// ParseGitHubTarget parses raw GitHub URLs or shorthand owner/repo notation.
func ParseGitHubTarget(input string) (*GitHubTarget, bool) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil, false
	}

	// Remove protocols
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "git@github.com:")

	// Handle raw.githubusercontent.com URLs
	if strings.HasPrefix(raw, "raw.githubusercontent.com/") {
		raw = strings.TrimPrefix(raw, "raw.githubusercontent.com/")
		raw = strings.Trim(raw, "/")
		parts := strings.Split(raw, "/")
		if len(parts) < 3 {
			return nil, false
		}
		owner := parts[0]
		repo := strings.TrimSuffix(parts[1], ".git")
		ref := parts[2]
		subpath := ""
		if len(parts) > 3 {
			subpath = strings.Join(parts[3:], "/")
		}
		if strings.HasSuffix(strings.ToLower(subpath), "/skill.md") {
			subpath = subpath[:len(subpath)-len("/skill.md")]
		} else if strings.EqualFold(subpath, "skill.md") {
			subpath = ""
		}
		return &GitHubTarget{
			Owner:       owner,
			Repo:        repo,
			Ref:         ref,
			Subpath:     subpath,
			OriginalURL: fmt.Sprintf("https://github.com/%s/%s", owner, repo),
		}, true
	}

	raw = strings.TrimPrefix(raw, "github.com/")

	// Extract ref if passed via @ref or #ref
	ref := "main"
	if idx := strings.IndexAny(raw, "@#"); idx != -1 {
		ref = strings.Trim(raw[idx+1:], "/")
		raw = raw[:idx]
	}

	raw = strings.Trim(raw, "/")
	parts := strings.Split(raw, "/")
	if len(parts) < 2 {
		return nil, false
	}

	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")

	// Validate owner/repo naming (basic sanity check)
	if owner == "" || repo == "" || strings.HasPrefix(owner, "-") || strings.HasPrefix(repo, "-") {
		return nil, false
	}

	subpath := ""
	if len(parts) > 2 {
		remainder := parts[2:]
		// Check for /tree/{ref}/subpath or /blob/{ref}/subpath
		if len(remainder) >= 2 && (remainder[0] == "tree" || remainder[0] == "blob") {
			ref = remainder[1]
			if len(remainder) > 2 {
				subpath = strings.Join(remainder[2:], "/")
			}
		} else {
			subpath = strings.Join(remainder, "/")
		}
	}

	// If subpath points directly to SKILL.md, trim it to directory
	if strings.HasSuffix(strings.ToLower(subpath), "/skill.md") {
		subpath = subpath[:len(subpath)-len("/skill.md")]
	} else if strings.EqualFold(subpath, "skill.md") {
		subpath = ""
	}

	return &GitHubTarget{
		Owner:       owner,
		Repo:        repo,
		Ref:         ref,
		Subpath:     subpath,
		OriginalURL: fmt.Sprintf("https://github.com/%s/%s", owner, repo),
	}, true
}

// FetchGitHubSkill attempts to fetch and parse SKILL.md from a GitHub repository via raw HTTP.
func FetchGitHubSkill(ctx context.Context, target *GitHubTarget, client *http.Client) (*Item, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	refsToTry := []string{target.Ref}
	if target.Ref == "main" {
		refsToTry = append(refsToTry, "master")
	}

	var lastErr error
	for _, ref := range refsToTry {
		item, err := probeGitHubRef(ctx, target, ref, client)
		if err == nil && item != nil {
			return item, nil
		}
		if err != nil {
			lastErr = err
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no valid SKILL.md found in github repository %s/%s (ref: %s)", target.Owner, target.Repo, target.Ref)
}

func probeGitHubRef(ctx context.Context, target *GitHubTarget, ref string, client *http.Client) (*Item, error) {
	var candidatePaths []string

	if target.Subpath != "" {
		candidatePaths = append(candidatePaths,
			target.Subpath+"/SKILL.md",
			target.Subpath+"/skill.md",
			"skills/"+target.Subpath+"/SKILL.md",
			"skills/"+target.Subpath+"/skill.md",
			".agents/skills/"+target.Subpath+"/SKILL.md",
			".agents/skills/"+target.Subpath+"/skill.md",
		)
		if strings.HasSuffix(strings.ToLower(target.Subpath), ".md") {
			candidatePaths = append([]string{target.Subpath}, candidatePaths...)
		}
	} else {
		candidatePaths = append(candidatePaths,
			"SKILL.md",
			"skill.md",
			"skills/"+target.Repo+"/SKILL.md",
			".agents/skills/"+target.Repo+"/SKILL.md",
		)
	}

	for _, relPath := range candidatePaths {
		rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", target.Owner, target.Repo, ref, relPath)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", version.UserAgent())

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB limit
		_ = resp.Body.Close()
		if err != nil || len(bodyBytes) == 0 {
			continue
		}

		fm, _, err := ParseFrontmatter(bodyBytes)
		if err != nil || fm == nil {
			fm = &Frontmatter{}
		}

		skillName := strings.TrimSpace(fm.Name)
		if skillName == "" {
			if target.Subpath != "" {
				skillName = filepath.Base(target.Subpath)
			} else {
				skillName = target.Repo
			}
		}

		item := Item{
			ID:           skillName,
			Name:         skillName,
			Type:         ItemTypeSkill,
			Description:  fm.Description,
			AllowedTools: fm.AllowedTools,
			License:      fm.License,
			Body:         string(bodyBytes),
			URL:          rawURL,
			RelativePath: relPath,
			Source:       "github",
			Metadata: map[string]string{
				"origin":    target.FullRepoURL(),
				"repo":      target.RepoString(),
				"ref":       ref,
				"canonical": rawURL,
			},
		}

		if fm.Metadata != nil {
			for k, v := range fm.Metadata {
				item.Metadata[k] = v
			}
		}
		if item.Metadata["version"] != "" {
			item.Version = item.Metadata["version"]
		}
		if item.Metadata["category"] != "" {
			item.Category = item.Metadata["category"]
		}
		if item.Metadata["tags"] != "" {
			item.Tags = ParseTags(item.Metadata["tags"])
		}
		if item.License == "" && fm.License != "" {
			item.License = fm.License
		}

		item.Normalize()
		return &item, nil
	}

	return nil, nil
}

type githubTreeResponse struct {
	Sha       string `json:"sha"`
	Truncated bool   `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"` // "blob", "tree"
		Sha  string `json:"sha"`
		Size int    `json:"size"`
		URL  string `json:"url"`
	} `json:"tree"`
}

// FetchGitHubCatalog discovers skills across a GitHub repository and returns a Manifest.
// The discovery order is:
// 1. catalog.json (skills.danicat.dev standard)
// 2. index.json (google/skills standard)
// 3. Directory crawl / Git Tree API (agentskills.io standard)
func FetchGitHubCatalog(ctx context.Context, target *GitHubTarget, client *http.Client) (*Manifest, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	refsToTry := []string{target.Ref}
	if target.Ref == "main" {
		refsToTry = append(refsToTry, "master")
	}

	var lastErr error
	for _, ref := range refsToTry {
		manifest, err := probeGitHubCatalogRef(ctx, target, ref, client)
		if err == nil && manifest != nil && len(manifest.Items) > 0 {
			return manifest, nil
		}
		if err != nil {
			lastErr = err
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no skills found in github repository %s/%s", target.Owner, target.Repo)
}

func tryFetchManifest(ctx context.Context, target *GitHubTarget, ref, catPath string, client *http.Client) (*Manifest, error) {
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", target.Owner, target.Repo, ref, catPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5MB limit
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("empty or unreadable body: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil || len(m.Items) == 0 {
		return nil, fmt.Errorf("invalid manifest JSON: %w", err)
	}

	for i := range m.Items {
		m.Items[i].Normalize()
		if m.Items[i].Source == "" {
			m.Items[i].Source = fmt.Sprintf("github:%s/%s", target.Owner, target.Repo)
		}
	}
	return &m, nil
}

func probeGitHubCatalogRef(ctx context.Context, target *GitHubTarget, ref string, client *http.Client) (*Manifest, error) {
	// 1. Try catalog.json endpoints first (skills.danicat.dev standard)
	candidateCatalogPaths := []string{
		"catalog.json",
		"skills/catalog.json",
	}
	if target.Subpath != "" {
		candidateCatalogPaths = append([]string{target.Subpath + "/catalog.json"}, candidateCatalogPaths...)
	}

	for _, catPath := range candidateCatalogPaths {
		if m, err := tryFetchManifest(ctx, target, ref, catPath, client); err == nil && m != nil && len(m.Items) > 0 {
			return m, nil
		}
	}

	// 2. Try index.json endpoints next (google/skills standard)
	candidateIndexPaths := []string{
		"index.json",
		"skills/index.json",
	}
	if target.Subpath != "" {
		candidateIndexPaths = append([]string{target.Subpath + "/index.json"}, candidateIndexPaths...)
	}

	for _, indexPath := range candidateIndexPaths {
		if m, err := tryFetchManifest(ctx, target, ref, indexPath, client); err == nil && m != nil && len(m.Items) > 0 {
			return m, nil
		}
	}

	// 3. Discover SKILL.md files via GitHub Git Tree API (agentskills.io standard directory crawl)
	treeURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1", target.Owner, target.Repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, treeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching git tree for %s/%s: %w", target.Owner, target.Repo, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d for tree of %s/%s", resp.StatusCode, target.Owner, target.Repo)
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
	if err != nil {
		return nil, fmt.Errorf("reading tree response: %w", err)
	}

	var treeResp githubTreeResponse
	if err := json.Unmarshal(bodyBytes, &treeResp); err != nil {
		return nil, fmt.Errorf("parsing git tree: %w", err)
	}

	var skillPaths []string
	prefix := ""
	if target.Subpath != "" {
		prefix = strings.Trim(target.Subpath, "/") + "/"
	}

	for _, node := range treeResp.Tree {
		if node.Type != "blob" {
			continue
		}
		lower := strings.ToLower(node.Path)
		if strings.HasSuffix(lower, "/skill.md") || lower == "skill.md" {
			if prefix == "" || strings.HasPrefix(node.Path, prefix) {
				skillPaths = append(skillPaths, node.Path)
			}
		}
	}

	if len(skillPaths) == 0 {
		return nil, fmt.Errorf("no SKILL.md files discovered in %s/%s", target.Owner, target.Repo)
	}

	manifest := &Manifest{
		Version: "1.0.0",
		Items:   make([]Item, 0, len(skillPaths)),
	}

	for _, relPath := range skillPaths {
		rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", target.Owner, target.Repo, ref, relPath)
		skillReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			continue
		}
		skillReq.Header.Set("User-Agent", version.UserAgent())

		skillResp, err := client.Do(skillReq)
		if err != nil {
			continue
		}
		if skillResp.StatusCode != http.StatusOK {
			_ = skillResp.Body.Close()
			continue
		}

		skillData, err := io.ReadAll(io.LimitReader(skillResp.Body, 2<<20))
		_ = skillResp.Body.Close()
		if err != nil || len(skillData) == 0 {
			continue
		}

		fm, _, _ := ParseFrontmatter(skillData)
		if fm == nil {
			fm = &Frontmatter{}
		}

		dirPath := filepath.Dir(relPath)
		if dirPath == "." {
			dirPath = ""
		}

		skillName := strings.TrimSpace(fm.Name)
		if skillName == "" {
			if dirPath != "" {
				skillName = filepath.Base(dirPath)
			} else {
				skillName = target.Repo
			}
		}

		sum := sha256.Sum256(skillData)
		digest := hex.EncodeToString(sum[:])

		githubTreeURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", target.Owner, target.Repo, ref)
		if dirPath != "" {
			githubTreeURL += "/" + dirPath
		}

		category := ""
		if fm.Metadata != nil {
			category = fm.Metadata["category"]
		}
		if category == "" {
			parts := strings.Split(relPath, "/")
			if len(parts) >= 3 {
				category = parts[len(parts)-3]
			} else {
				category = "agents"
			}
		}

		item := Item{
			ID:           skillName,
			Name:         skillName,
			Type:         ItemTypeSkill,
			Description:  fm.Description,
			Category:     category,
			License:      fm.License,
			AllowedTools: fm.AllowedTools,
			Body:         string(skillData),
			URL:          githubTreeURL,
			RelativePath: relPath,
			Digest:       digest,
			Sha256:       digest,
			Source:       fmt.Sprintf("github:%s/%s", target.Owner, target.Repo),
			Metadata: map[string]string{
				"origin":    target.FullRepoURL(),
				"repo":      target.RepoString(),
				"ref":       ref,
				"canonical": rawURL,
			},
		}
		if fm.Metadata != nil {
			for k, v := range fm.Metadata {
				item.Metadata[k] = v
			}
			if item.Metadata["version"] != "" {
				item.Version = item.Metadata["version"]
			}
			if item.Metadata["author"] != "" {
				item.Author = item.Metadata["author"]
			}
			if item.Metadata["tags"] != "" {
				item.Tags = ParseTags(item.Metadata["tags"])
			}
		}
		item.Normalize()
		manifest.Items = append(manifest.Items, item)
	}

	return manifest, nil
}

// FetchHTTPCatalog fetches a remote catalog manifest via HTTP GET.
// Discovery order:
// 1. catalog.json (skills.danicat.dev standard)
// 2. index.json (google/skills standard)
func FetchHTTPCatalog(ctx context.Context, rawURL string, client *http.Client) (*Manifest, string, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	var endpointsToTry []string
	if strings.HasSuffix(strings.ToLower(rawURL), ".json") {
		endpointsToTry = []string{rawURL}
	} else {
		endpointsToTry = []string{
			ResolveCatalogURL(rawURL),
			strings.TrimRight(rawURL, "/") + "/index.json",
		}
	}

	var lastErr error
	for _, endpoint := range endpointsToTry {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", version.UserAgent())
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("fetching catalog from %s: %w", endpoint, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d fetching catalog from %s", resp.StatusCode, endpoint)
			continue
		}

		etag := resp.Header.Get("ETag")
		data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("reading catalog response: %w", err)
			continue
		}

		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			lastErr = fmt.Errorf("parsing catalog manifest from %s: %w", endpoint, err)
			continue
		}

		for i := range manifest.Items {
			manifest.Items[i].Normalize()
			if manifest.Items[i].Source == "" {
				manifest.Items[i].Source = endpoint
			}
		}

		return &manifest, etag, nil
	}

	return nil, "", lastErr
}

// DiscoverLocalCatalog discovers skills inside a local filesystem directory.
// Discovery order:
// 1. catalog.json (skills.danicat.dev standard)
// 2. index.json (google/skills standard)
// 3. Directory crawl (agentskills.io standard)
func DiscoverLocalCatalog(dirPath string) (*Manifest, error) {
	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", absDir)
	}

	// 1. catalog.json (skills.danicat.dev standard)
	catPath := filepath.Join(absDir, "catalog.json")
	if data, err := os.ReadFile(catPath); err == nil {
		var m Manifest
		if err := json.Unmarshal(data, &m); err == nil && len(m.Items) > 0 {
			for i := range m.Items {
				m.Items[i].Normalize()
				if m.Items[i].Source == "" {
					m.Items[i].Source = absDir
				}
			}
			return &m, nil
		}
	}

	// 2. index.json (google/skills standard)
	indexPath := filepath.Join(absDir, "index.json")
	if data, err := os.ReadFile(indexPath); err == nil {
		var m Manifest
		if err := json.Unmarshal(data, &m); err == nil && len(m.Items) > 0 {
			for i := range m.Items {
				m.Items[i].Normalize()
				if m.Items[i].Source == "" {
					m.Items[i].Source = absDir
				}
			}
			return &m, nil
		}
	}

	// 3. Directory crawl (agentskills.io standard)
	var items []Item
	err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), "skill.md") {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			fm, _, _ := ParseFrontmatter(content)
			if fm == nil {
				fm = &Frontmatter{}
			}

			skillDir := filepath.Dir(path)
			relDir, _ := filepath.Rel(absDir, skillDir)
			skillName := strings.TrimSpace(fm.Name)
			if skillName == "" {
				skillName = filepath.Base(skillDir)
			}

			sum := sha256.Sum256(content)
			digest := hex.EncodeToString(sum[:])

			category := ""
			if fm.Metadata != nil {
				category = fm.Metadata["category"]
			}
			if category == "" {
				category = "agents"
			}

			item := Item{
				ID:           skillName,
				Name:         skillName,
				Type:         ItemTypeSkill,
				Description:  fm.Description,
				Category:     category,
				License:      fm.License,
				AllowedTools: fm.AllowedTools,
				Body:         string(content),
				Path:         skillDir,
				RelativePath: relDir,
				Digest:       digest,
				Sha256:       digest,
				Source:       absDir,
			}
			if fm.Metadata != nil {
				if item.Metadata == nil {
					item.Metadata = make(map[string]string)
				}
				for k, v := range fm.Metadata {
					item.Metadata[k] = v
				}
				if item.Metadata["version"] != "" {
					item.Version = item.Metadata["version"]
				}
				if item.Metadata["author"] != "" {
					item.Author = item.Metadata["author"]
				}
				if item.Metadata["tags"] != "" {
					item.Tags = ParseTags(item.Metadata["tags"])
				}
			}
			item.Normalize()
			items = append(items, item)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("discovering local skills in %s: %w", absDir, err)
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("no SKILL.md files found in %s", absDir)
	}

	return &Manifest{
		Version: "1.0.0",
		Items:   items,
	}, nil
}
