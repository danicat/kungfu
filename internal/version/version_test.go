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

package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	t.Parallel()
	v := Get()
	if v == "" {
		t.Fatal("Get() returned empty version string")
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		override string
		fallback string
		info     *debug.BuildInfo
		want     string
	}{
		{
			name:     "ldflags override wins over build info",
			override: "v1.2.3",
			fallback: "0.8.1",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}},
			want:     "1.2.3",
		},
		{
			name:     "clean tagged release",
			fallback: "0.8.1",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}},
			want:     "0.9.0",
		},
		{
			name:     "tagged prerelease containing -0.20",
			fallback: "0.8.1",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "v1.0.0-0.20.0"}},
			want:     "1.0.0-0.20.0",
		},
		{
			name:     "pseudo-version from main branch",
			fallback: "0.8.1",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "v0.8.2-0.20261125120000-abcdef123456"}},
			want:     "0.8.1-dev+abcdef1",
		},
		{
			name:     "dirty pseudo-version",
			fallback: "0.8.1",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261125120000-abcdef123456+dirty"}},
			want:     "0.8.1-dev+abcdef1+dirty",
		},
		{
			name:     "devel build with clean vcs revision",
			fallback: "0.8.1",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "1234567890abcdef"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			want: "0.8.1-dev+1234567",
		},
		{
			name:     "devel build with dirty vcs revision",
			fallback: "0.8.1",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "1234567890abcdef"},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			want: "0.8.1-dev+1234567.dirty",
		},
		{
			name:     "nil build info falls back",
			fallback: "0.8.1",
			info:     nil,
			want:     "0.8.1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolve(tc.override, tc.fallback, tc.info); got != tc.want {
				t.Errorf("resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserAgent(t *testing.T) {
	t.Parallel()
	ua := UserAgent()
	if !strings.HasPrefix(ua, "kungfu/") {
		t.Fatalf("UserAgent() = %q, want prefix 'kungfu/'", ua)
	}
}
