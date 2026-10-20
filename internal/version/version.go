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

package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// DefaultVersion is the fallback version string used in local development builds.
var DefaultVersion = "0.6.0"

// Get returns the dynamic version string resolved from runtime/debug build info
// (e.g., when built via `go install module@version`), falling back to DefaultVersion.
func Get() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			if strings.Contains(info.Main.Version, "dirty") {
				return DefaultVersion + "+dirty"
			}
			return strings.TrimPrefix(info.Main.Version, "v")
		}
	}
	return DefaultVersion
}

// UserAgent returns the standard User-Agent header for HTTP requests.
func UserAgent() string {
	return fmt.Sprintf("kungfu/%s", Get())
}
