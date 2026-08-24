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
	"io/fs"
	"path/filepath"
	"strings"
)

// ParseSkillFromBytes parses raw SKILL.md bytes and returns a populated Item.
func ParseSkillFromBytes(data []byte, relPath string) (Item, error) {
	fm, body, err := ParseFrontmatter(data)
	if err != nil {
		return Item{}, err
	}
	id := ""
	if fm != nil && fm.Name != "" {
		id = fm.Name
	}
	if id == "" && relPath != "" {
		dir := filepath.Dir(relPath)
		if dir != "." && dir != "" {
			id = filepath.Base(dir)
		} else {
			id = strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
		}
	}
	if id == "" {
		id = "unknown"
	}

	category := "general"
	if fm != nil && fm.Metadata != nil && fm.Metadata["category"] != "" {
		category = fm.Metadata["category"]
	}
	var tags []string
	if fm != nil && fm.Metadata != nil && fm.Metadata["tags"] != "" {
		tags = ParseTags(fm.Metadata["tags"])
	} else {
		tags = []string{"agent-skills"}
	}
	description := ""
	license := ""
	allowedTools := ""
	var metadata map[string]string
	if fm != nil {
		description = fm.Description
		license = fm.License
		allowedTools = fm.AllowedTools
		metadata = fm.Metadata
	}
	author := ""
	version := "0.1.0"
	if metadata != nil {
		author = metadata["author"]
		if v := metadata["version"]; v != "" {
			version = v
		}
	}

	return Item{
		ID:           id,
		Name:         id,
		Type:         ItemTypeSkill,
		Title:        id,
		Description:  description,
		Category:     category,
		Tags:         tags,
		Author:       author,
		Version:      version,
		License:      license,
		AllowedTools: allowedTools,
		Body:         body,
		Metadata:     metadata,
		Digest:       ComputeDigest(data),
		Source:       "embedded",
	}, nil
}

// DiscoverSkillsInFS walks an fs.FS and returns all discovered skill items mapped to their raw content.
func DiscoverSkillsInFS(fsys fs.FS) ([]Item, map[string]string, error) {
	if fsys == nil {
		return nil, nil, nil
	}
	var items []Item
	contents := make(map[string]string)

	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := strings.ToLower(d.Name())
		if base == "skill.md" || strings.HasSuffix(base, ".md") {
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				return nil
			}
			if strings.HasPrefix(string(data), "---") {
				item, err := ParseSkillFromBytes(data, path)
				if err == nil && item.ID != "" {
					items = append(items, item)
					contents[strings.ToLower(item.ID)] = string(data)
					contents[strings.ToLower(item.Name)] = string(data)
				}
			}
		}
		return nil
	})

	if err != nil {
		return nil, nil, err
	}
	return items, contents, nil
}
