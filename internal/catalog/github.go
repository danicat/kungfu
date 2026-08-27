package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
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

// IsGitHubTarget checks if the input is a GitHub URL or owner/repo reference.
func IsGitHubTarget(input string) bool {
	_, ok := ParseGitHubTarget(input)
	return ok
}

// ParseGitHubTarget parses raw GitHub URLs or shorthand owner/repo notation.
// Examples:
//   - https://github.com/owner/repo
//   - https://github.com/owner/repo.git
//   - github.com/owner/repo
//   - https://raw.githubusercontent.com/owner/repo/branch/subpath/SKILL.md
//   - owner/repo
//   - owner/repo/skill-name
//   - owner/repo/skills/skill-name
//   - https://github.com/owner/repo/tree/main/skills/foo
//   - https://github.com/owner/repo/blob/main/skills/foo/SKILL.md
//   - owner/repo@v1.0.0
//   - owner/repo#branch
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
		req.Header.Set("User-Agent", "kungfu/0.2.0")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB limit
		resp.Body.Close()
		if err != nil || len(bodyBytes) == 0 {
			continue
		}

		fm, _, err := ParseFrontmatter(bodyBytes)
		if err != nil || fm == nil {
			// Even if frontmatter parse fails, we have valid content
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

		dirPath := filepath.Dir(relPath)
		if dirPath == "." {
			dirPath = ""
		}

		githubTreeURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", target.Owner, target.Repo, ref)
		if dirPath != "" {
			githubTreeURL += "/" + dirPath
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
			GithubURL:    githubTreeURL,
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
