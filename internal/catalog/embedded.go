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
	_ "embed"
)

//go:embed embedded/SKILL.md
var EmbeddedKungFuSkill []byte

// GetEmbeddedKungFuItem parses and returns the embedded kungfu Skill item.
func GetEmbeddedKungFuItem() (Item, error) {
	fm, body, err := ParseFrontmatter(EmbeddedKungFuSkill)
	if err != nil {
		return Item{}, err
	}
	id := "kungfu"
	if fm != nil && fm.Name != "" {
		id = fm.Name
	}
	category := "gateway"
	if fm != nil && fm.Metadata["category"] != "" {
		category = fm.Metadata["category"]
	}
	var tags []string
	if fm != nil && fm.Metadata["tags"] != "" {
		tags = ParseTags(fm.Metadata["tags"])
	} else {
		tags = []string{"agent-skills", "discovery", "search", "catalog", "okf", "cli", "learn"}
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
		Digest:       ComputeDigest(EmbeddedKungFuSkill),
		Source:       "embedded",
	}, nil
}
