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
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
)

// Version may be injected at build time via:
//
//	go build -ldflags "-X github.com/danicat/kungfu/internal/version.Version=1.0.0"
var Version = ""

// DefaultVersion is the fallback release version string when neither -ldflags
// nor tagged module build info is available.
var DefaultVersion = "0.9.1"

var pseudoVersionRe = regexp.MustCompile(`(?:^v0\.0\.0-|-0\.|\.)\d{14}-([0-9a-f]{12})(\+dirty)?$`)

// Get returns the version string resolved in priority order:
// 1. Build-time -ldflags override (Version)
// 2. Tagged module version from runtime/debug.ReadBuildInfo
// 3. DefaultVersion annotated with VCS revision / dirty status for dev builds
func Get() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}
	return resolve(Version, DefaultVersion, info)
}

func resolve(override, fallback string, info *debug.BuildInfo) string {
	if v := strings.TrimSpace(override); v != "" {
		return strings.TrimPrefix(v, "v")
	}

	if info != nil {
		v := strings.TrimSpace(info.Main.Version)
		if v != "" && v != "(devel)" {
			if m := pseudoVersionRe.FindStringSubmatch(v); m != nil {
				shortRev := m[1]
				if len(shortRev) > 7 {
					shortRev = shortRev[:7]
				}
				return fallback + "-dev+" + shortRev + m[2]
			}
			return strings.TrimPrefix(v, "v")
		}

		var rev string
		var modified bool
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = strings.TrimSpace(s.Value)
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 7 {
				rev = rev[:7]
			}
			out := fallback + "-dev+" + rev
			if modified {
				out += ".dirty"
			}
			return out
		}
		if modified {
			return fallback + "+dirty"
		}
	}

	return fallback
}

// UserAgent returns the standard User-Agent header for HTTP requests.
func UserAgent() string {
	return fmt.Sprintf("kungfu/%s", Get())
}
