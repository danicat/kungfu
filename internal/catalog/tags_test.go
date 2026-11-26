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
	"testing"
)

func TestParseTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"go", []string{"go"}},
		{"go, golang, testing", []string{"go", "golang", "testing"}},
		{"go; python\nrust", []string{"go", "python", "rust"}},
		{"GO, go, Go", []string{"go"}},
	}

	for _, tt := range tests {
		got := ParseTags(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("ParseTags(%q) length = %d, want %d (%v vs %v)", tt.input, len(got), len(tt.want), got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseTags(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}
