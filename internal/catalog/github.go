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

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/danicat/kungfu/internal/version"
)

const maxTreeCrawlSkills = 200

var githubIdentRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// GitHubTarget captures parsed repository and subpath components.
type GitHubTarget struct {
	Owner       string // e.g. "anthropics"
	Repo        string // e.g. "anthropic-quickstarts"
	Ref         string // e.g. "HEAD", "main", "v1.0.0"
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

// CanonicalSource returns a round-trippable catalog source string for config.yaml.
func (t *GitHubTarget) CanonicalSource() string {
	base := t.RepoString()
	if t.Subpath != "" {
		base += "/" + strings.Trim(t.Subpath, "/")
	}
	if t.Ref != "" && t.Ref != "HEAD" {
		base += "@" + t.Ref
	}
	return base
}

func isValidGitHubIdent(s string) bool {
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "-") {
		return false
	}
	return githubIdentRe.MatchString(s)
}

func isValidGitHubPathOrRef(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r == '?' || r == '#' || r == '\\' || r == 0 || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	for seg := range strings.SplitSeq(s, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

func escapePathSegments(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

func buildGitHubRawURL(owner, repo, ref, relPath string) string {
	return fmt.Sprintf(
		"https://raw.githubusercontent.com/%s/%s/%s/%s",
		url.PathEscape(owner),
		url.PathEscape(repo),
		escapePathSegments(ref),
		escapePathSegments(relPath),
	)
}

func trimSkillMDSubpath(subpath string) string {
	if strings.HasSuffix(strings.ToLower(subpath), "/skill.md") {
		return subpath[:len(subpath)-len("/skill.md")]
	}
	if strings.EqualFold(subpath, "skill.md") {
		return ""
	}
	return subpath
}

// ParseGitHubTarget parses GitHub URLs or shorthand owner/repo[/subpath][@ref] notation with strict validation.
func ParseGitHubTarget(input string) (*GitHubTarget, bool) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil, false
	}

	var pathStr string
	var isRawHost bool
	ref := "HEAD"

	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.RawQuery != "" {
			return nil, false
		}
		host := strings.ToLower(u.Hostname())
		switch host {
		case "github.com":
			pathStr = strings.Trim(u.Path, "/")
			if u.Fragment != "" {
				ref = strings.Trim(u.Fragment, "/")
			}
		case "raw.githubusercontent.com":
			isRawHost = true
			pathStr = strings.Trim(u.Path, "/")
		default:
			return nil, false
		}
	} else if after, ok := strings.CutPrefix(raw, "git@github.com:"); ok {
		pathStr = strings.Trim(after, "/")
	} else if after, ok := strings.CutPrefix(raw, "raw.githubusercontent.com/"); ok {
		isRawHost = true
		pathStr = strings.Trim(after, "/")
	} else if after, ok := strings.CutPrefix(raw, "github.com/"); ok {
		pathStr = strings.Trim(after, "/")
	} else {
		pathStr = raw
	}

	if isRawHost {
		parts := strings.Split(pathStr, "/")
		if len(parts) < 3 {
			return nil, false
		}
		owner := parts[0]
		repo := strings.TrimSuffix(parts[1], ".git")
		ref = parts[2]
		subpath := ""
		if len(parts) > 3 {
			subpath = strings.Join(parts[3:], "/")
		}
		subpath = trimSkillMDSubpath(subpath)
		if !isValidGitHubIdent(owner) || !isValidGitHubIdent(repo) || ref == "" || !isValidGitHubPathOrRef(ref) || !isValidGitHubPathOrRef(subpath) {
			return nil, false
		}
		return &GitHubTarget{
			Owner:       owner,
			Repo:        repo,
			Ref:         ref,
			Subpath:     subpath,
			OriginalURL: fmt.Sprintf("https://github.com/%s/%s", owner, repo),
		}, true
	}

	// Extract @ref or #ref shorthand if present.
	if idx := strings.IndexAny(pathStr, "@#"); idx != -1 {
		refPart := strings.Trim(pathStr[idx+1:], "/")
		if refPart == "" {
			return nil, false
		}
		ref = refPart
		pathStr = pathStr[:idx]
	}

	pathStr = strings.Trim(pathStr, "/")
	parts := strings.Split(pathStr, "/")
	if len(parts) < 2 {
		return nil, false
	}

	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	if !isValidGitHubIdent(owner) || !isValidGitHubIdent(repo) {
		return nil, false
	}

	subpath := ""
	if len(parts) > 2 {
		remainder := parts[2:]
		if len(remainder) >= 2 && (remainder[0] == "tree" || remainder[0] == "blob") {
			ref = remainder[1]
			if len(remainder) > 2 {
				subpath = strings.Join(remainder[2:], "/")
			}
		} else {
			subpath = strings.Join(remainder, "/")
		}
	}

	subpath = trimSkillMDSubpath(subpath)
	if ref == "" || !isValidGitHubPathOrRef(ref) || !isValidGitHubPathOrRef(subpath) {
		return nil, false
	}

	return &GitHubTarget{
		Owner:       owner,
		Repo:        repo,
		Ref:         ref,
		Subpath:     subpath,
		OriginalURL: fmt.Sprintf("https://github.com/%s/%s", owner, repo),
	}, true
}

func githubAPIToken() string {
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		return tok
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

func githubRateLimitError(resp *http.Response, targetURL string) error {
	rem := resp.Header.Get("X-RateLimit-Remaining")
	reset := resp.Header.Get("X-RateLimit-Reset")
	detail := ""
	if rem == "0" || resp.StatusCode == http.StatusTooManyRequests {
		if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
			detail = fmt.Sprintf("GitHub API rate limit exceeded (resets at %s; set GITHUB_TOKEN to raise limit)", time.Unix(ts, 0).UTC().Format(time.RFC3339))
		} else {
			detail = "GitHub API rate limit exceeded (set GITHUB_TOKEN to raise limit)"
		}
	}
	return &HTTPStatusError{StatusCode: resp.StatusCode, URL: targetURL, Detail: detail}
}

// FetchGitHubSkill fetches and parses a single SKILL.md from a GitHub repository.
func FetchGitHubSkill(ctx context.Context, target *GitHubTarget, client *http.Client) (*Item, error) {
	if client == nil {
		client = NewSafeHTTPClient(15 * time.Second)
	}
	ref := target.Ref
	if ref == "" {
		ref = "HEAD"
	}

	item, err := probeGitHubRef(ctx, target, ref, client)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, fmt.Errorf("%w: no valid SKILL.md found in github repository %s/%s (ref: %s)", ErrItemNotFound, target.Owner, target.Repo, ref)
	}
	return item, nil
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
		rawURL := buildGitHubRawURL(target.Owner, target.Repo, ref, relPath)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", version.UserAgent())

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching skill from github %s/%s: %w", target.Owner, target.Repo, err)
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			err := githubRateLimitError(resp, rawURL)
			_ = resp.Body.Close()
			return nil, err
		}

		bodyBytes, err := ReadBounded(resp.Body, DefaultMaxSkillMDBytes)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", rawURL, err)
		}
		if len(bodyBytes) == 0 {
			return nil, fmt.Errorf("empty SKILL.md at %s", rawURL)
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
			Body:         string(bodyBytes),
			URL:          rawURL,
			RelativePath: relPath,
			Digest:       ComputeDigest(bodyBytes),
			Source:       "github",
			Metadata: map[string]string{
				"origin":    target.FullRepoURL(),
				"repo":      target.RepoString(),
				"ref":       ref,
				"canonical": rawURL,
			},
		}
		ApplyFrontmatter(&item, fm, true)
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
		Type string `json:"type"`
		Sha  string `json:"sha"`
		Size int    `json:"size"`
		URL  string `json:"url"`
	} `json:"tree"`
}

// FetchGitHubCatalog discovers skills across a GitHub repository and returns a Manifest.
// Discovery order:
// 1. .well-known/agent-skills/index.json (Cloudflare Agent Skills Discovery RFC v0.2.0)
// 2. index.json (google/skills standard)
// 3. Git Tree API directory crawl (agentskills.io standard)
func FetchGitHubCatalog(ctx context.Context, target *GitHubTarget, client *http.Client) (*Manifest, error) {
	if client == nil {
		client = NewSafeHTTPClient(30 * time.Second)
	}
	ref := target.Ref
	if ref == "" {
		ref = "HEAD"
	}

	manifest, err := probeGitHubCatalogRef(ctx, target, ref, client)
	if err != nil {
		return nil, err
	}
	if manifest == nil || len(manifest.Items) == 0 {
		return nil, fmt.Errorf("no skills found in github repository %s/%s", target.Owner, target.Repo)
	}
	return manifest, nil
}

var errManifestNotFound = errors.New("manifest not found")

func tryFetchManifest(ctx context.Context, target *GitHubTarget, ref, catPath string, client *http.Client) (*Manifest, error) {
	rawURL := buildGitHubRawURL(target.Owner, target.Repo, ref, catPath)
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

	if resp.StatusCode == http.StatusNotFound {
		return nil, errManifestNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, githubRateLimitError(resp, rawURL)
	}

	data, err := ReadBounded(resp.Body, 5<<20)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", rawURL, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty manifest body at %s", rawURL)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest JSON at %s: %w", rawURL, err)
	}
	if len(m.Items) == 0 {
		return nil, fmt.Errorf("manifest at %s contains no skills", rawURL)
	}

	allowLoopback := AllowLoopbackHTTP(client)
	for i := range m.Items {
		if m.Items[i].URL != "" && !strings.HasPrefix(m.Items[i].URL, "http://") && !strings.HasPrefix(m.Items[i].URL, "https://") {
			rel := strings.TrimPrefix(strings.TrimPrefix(m.Items[i].URL, "./"), "/")
			if !isValidGitHubPathOrRef(rel) {
				return nil, fmt.Errorf("invalid relative skill URL %q in %s", m.Items[i].URL, rawURL)
			}
			m.Items[i].URL = buildGitHubRawURL(target.Owner, target.Repo, ref, rel)
		} else if m.Items[i].URL != "" {
			if _, err := ValidateRemoteURL(m.Items[i].URL, allowLoopback); err != nil {
				return nil, fmt.Errorf("skill %q URL in %s rejected: %w", m.Items[i].Name, rawURL, err)
			}
		}
		m.Items[i].Normalize()
		if m.Items[i].Source == "" {
			m.Items[i].Source = fmt.Sprintf("github:%s/%s", target.Owner, target.Repo)
		}
	}
	return &m, nil
}

func probeGitHubCatalogRef(ctx context.Context, target *GitHubTarget, ref string, client *http.Client) (*Manifest, error) {
	var manifestPaths []string
	if target.Subpath != "" {
		manifestPaths = append(manifestPaths,
			target.Subpath+"/.well-known/agent-skills/index.json",
			".well-known/agent-skills/index.json",
			target.Subpath+"/index.json",
			"index.json",
			"skills/index.json",
		)
	} else {
		manifestPaths = []string{
			".well-known/agent-skills/index.json",
			"index.json",
			"skills/index.json",
		}
	}

	for _, p := range manifestPaths {
		m, err := tryFetchManifest(ctx, target, ref, p, client)
		if err == nil && m != nil {
			return m, nil
		}
		if errors.Is(err, errManifestNotFound) {
			continue
		}
		// Fail fast on non-404 errors (network failure, 403/429/5xx, malformed manifest JSON).
		return nil, err
	}

	// Discover SKILL.md files via GitHub Git Tree API (agentskills.io standard directory crawl).
	treeURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1",
		url.PathEscape(target.Owner),
		url.PathEscape(target.Repo),
		escapePathSegments(ref),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, treeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := githubAPIToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching git tree for %s/%s: %w", target.Owner, target.Repo, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, githubRateLimitError(resp, treeURL)
	}

	bodyBytes, err := ReadBounded(resp.Body, 10<<20)
	if err != nil {
		return nil, fmt.Errorf("reading tree response: %w", err)
	}

	var treeResp githubTreeResponse
	if err := json.Unmarshal(bodyBytes, &treeResp); err != nil {
		return nil, fmt.Errorf("parsing git tree: %w", err)
	}
	if treeResp.Truncated {
		return nil, fmt.Errorf("github git tree for %s/%s was truncated; publish .well-known/agent-skills/index.json or narrow with --subpath", target.Owner, target.Repo)
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
				if len(skillPaths) > maxTreeCrawlSkills {
					return nil, fmt.Errorf("repository %s/%s exceeds %d skills during tree crawl; publish .well-known/agent-skills/index.json", target.Owner, target.Repo, maxTreeCrawlSkills)
				}
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
		if !isValidGitHubPathOrRef(relPath) {
			continue
		}
		rawURL := buildGitHubRawURL(target.Owner, target.Repo, ref, relPath)
		skillResp, err := FetchWithRetryAndHeaders(ctx, client, rawURL, version.UserAgent(), nil, 2, defaultInitialBackoff)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
		}
		if skillResp.StatusCode != http.StatusOK {
			_ = skillResp.Body.Close()
			return nil, &HTTPStatusError{StatusCode: skillResp.StatusCode, URL: rawURL}
		}

		skillData, err := ReadBounded(skillResp.Body, DefaultMaxSkillMDBytes)
		_ = skillResp.Body.Close()
		if err != nil || len(skillData) == 0 {
			continue
		}

		fm, _, _ := ParseFrontmatter(skillData)
		if fm == nil {
			fm = &Frontmatter{}
		}

		dirPath := filepath.ToSlash(filepath.Dir(relPath))
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

		webURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", url.PathEscape(target.Owner), url.PathEscape(target.Repo), escapePathSegments(ref))
		if dirPath != "" {
			webURL += "/" + escapePathSegments(dirPath)
		}

		item := Item{
			ID:           skillName,
			Name:         skillName,
			Type:         ItemTypeSkill,
			Body:         string(skillData),
			URL:          rawURL,
			RelativePath: relPath,
			Digest:       ComputeDigest(skillData),
			Source:       fmt.Sprintf("github:%s/%s", target.Owner, target.Repo),
			Metadata: map[string]string{
				"origin":    target.FullRepoURL(),
				"repo":      target.RepoString(),
				"ref":       ref,
				"canonical": rawURL,
				"web_url":   webURL,
			},
		}
		ApplyFrontmatter(&item, fm, true)
		item.Normalize()
		manifest.Items = append(manifest.Items, item)
	}

	return manifest, nil
}

// FetchHTTPCatalog fetches a remote catalog manifest via HTTPS GET.
// Discovery order:
// 1. /.well-known/agent-skills/index.json (Cloudflare Agent Skills Discovery RFC v0.2.0)
// 2. /index.json (google/skills standard)
func FetchHTTPCatalog(ctx context.Context, rawURL string, client *http.Client) (*Manifest, string, error) {
	allowLoopback := AllowLoopbackHTTP(client)
	if client == nil {
		client = NewSafeHTTPClient(15 * time.Second)
	}

	trimmed := strings.TrimSpace(rawURL)
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	if _, err := ValidateRemoteURL(trimmed, allowLoopback); err != nil {
		return nil, "", err
	}

	var endpointsToTry []string
	if strings.HasSuffix(strings.ToLower(trimmed), ".json") {
		endpointsToTry = []string{trimmed}
	} else {
		base := strings.TrimRight(trimmed, "/")
		endpointsToTry = []string{
			base + "/.well-known/agent-skills/index.json",
			base + "/index.json",
		}
	}

	var lastErr error
	for _, endpoint := range endpointsToTry {
		headers := map[string]string{"Accept": "application/json"}
		resp, err := FetchWithRetryAndHeaders(ctx, client, endpoint, version.UserAgent(), headers, defaultMaxRetries, defaultInitialBackoff)
		if err != nil {
			return nil, "", fmt.Errorf("fetching catalog from %s: %w", endpoint, err)
		}

		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			lastErr = &HTTPStatusError{StatusCode: resp.StatusCode, URL: endpoint}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			statusErr := &HTTPStatusError{StatusCode: resp.StatusCode, URL: endpoint}
			_ = resp.Body.Close()
			return nil, "", statusErr
		}

		etag := resp.Header.Get("ETag")
		data, err := ReadBounded(resp.Body, 10<<20)
		_ = resp.Body.Close()
		if err != nil {
			return nil, "", fmt.Errorf("reading catalog response from %s: %w", endpoint, err)
		}

		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, "", fmt.Errorf("parsing catalog manifest from %s: %w", endpoint, err)
		}

		if err := ResolveManifestURLs(&manifest, endpoint, allowLoopback); err != nil {
			return nil, "", err
		}

		return &manifest, etag, nil
	}

	return nil, "", lastErr
}

// DiscoverLocalCatalog discovers skills inside a local filesystem directory.
// Discovery order:
// 1. .well-known/agent-skills/index.json
// 2. index.json
// 3. Directory crawl for SKILL.md files
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

	manifestCandidates := []string{
		filepath.Join(absDir, ".well-known", "agent-skills", "index.json"),
		filepath.Join(absDir, "index.json"),
	}
	for _, cand := range manifestCandidates {
		f, err := os.Open(cand)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", cand, err)
		}
		data, readErr := ReadBounded(f, 10<<20)
		_ = f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading %s: %w", cand, readErr)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", cand, err)
		}
		if len(m.Items) == 0 {
			return nil, fmt.Errorf("manifest %s contains no skills", cand)
		}
		for i := range m.Items {
			m.Items[i].Normalize()
			if m.Items[i].Source == "" {
				m.Items[i].Source = absDir
			}
		}
		return &m, nil
	}

	var items []Item
	err = filepath.Walk(absDir, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if !IsPathContained(absDir, path) {
				return nil
			}
		}
		if fi.IsDir() {
			return nil
		}
		if !strings.EqualFold(fi.Name(), "skill.md") {
			return nil
		}
		if !IsPathContained(absDir, path) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		content, readErr := ReadBounded(f, DefaultMaxSkillMDBytes)
		_ = f.Close()
		if readErr != nil {
			return readErr
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

		item := Item{
			ID:           skillName,
			Name:         skillName,
			Type:         ItemTypeSkill,
			Body:         string(content),
			Path:         skillDir,
			RelativePath: filepath.ToSlash(relDir),
			Digest:       ComputeDigest(content),
			Source:       absDir,
		}
		ApplyFrontmatter(&item, fm, true)
		item.Normalize()
		items = append(items, item)
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

// ClassifySource returns "local", "http", or "github" for a catalog source string.
func ClassifySource(source string) string {
	s := strings.TrimSpace(source)
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~") {
		return "local"
	}
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if strings.EqualFold(u.Hostname(), "github.com") || strings.EqualFold(u.Hostname(), "raw.githubusercontent.com") {
			return "github"
		}
		return "http"
	}
	if fi, err := os.Stat(s); err == nil && fi.IsDir() {
		return "local"
	}
	if _, ok := ParseGitHubTarget(s); ok {
		return "github"
	}
	return "http"
}

// FetchSource fetches a catalog manifest from a GitHub, HTTP, or local directory source.
// Returns the manifest, optional ETag, and resolved source type ("github", "http", or "local").
func FetchSource(ctx context.Context, source string, client *http.Client) (*Manifest, string, string, error) {
	kind := ClassifySource(source)
	switch kind {
	case "local":
		m, err := DiscoverLocalCatalog(source)
		return m, "", "local", err
	case "github":
		target, ok := ParseGitHubTarget(source)
		if !ok {
			return nil, "", "github", fmt.Errorf("invalid github catalog source %q", source)
		}
		m, err := FetchGitHubCatalog(ctx, target, client)
		return m, "", "github", err
	default:
		m, etag, err := FetchHTTPCatalog(ctx, source, client)
		return m, etag, "http", err
	}
}
