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

package runner

import (
	"github.com/danicat/kungfu/internal/catalog"
)

// Archive safety errors per Cloudflare Agent Skills Discovery RFC v0.2.0.
var (
	ErrArchiveTraversal         = catalog.ErrArchiveTraversal
	ErrArchiveSymlinkEscapes    = catalog.ErrArchiveSymlinkEscapes
	ErrArchiveDecompressionBomb = catalog.ErrArchiveDecompressionBomb
	ErrArchiveMissingSkillMD    = catalog.ErrArchiveMissingSkillMD
	ErrUnsupportedArchiveFormat = catalog.ErrUnsupportedArchiveFormat
)

// DefaultMaxUnpackedBytes limits total unpacked size to prevent decompression bombs (50 MB).
const DefaultMaxUnpackedBytes = catalog.DefaultMaxUnpackedBytes

// DetectArchiveFormat inspects file bytes or path/URL to determine if it is .tar.gz or .zip.
func DetectArchiveFormat(data []byte, nameOrURL string) string {
	return catalog.DetectArchiveFormat(data, nameOrURL)
}

// UnpackArchive extracts a .tar.gz or .zip archive into destDir with strict RFC v0.2.0 safety checks.
func UnpackArchive(data []byte, destDir string, maxUnpackedBytes int64) ([]string, error) {
	return catalog.UnpackArchive(data, destDir, maxUnpackedBytes)
}
