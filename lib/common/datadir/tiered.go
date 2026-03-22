/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package datadir

import (
	"path/filepath"

	"github.com/n42blockchain/N42/lib/common/dir"
)

// ApplyTierOverrides modifies a Dirs struct to use tiered storage paths.
// Empty paths are left unchanged (use default).
//
// Tier mapping:
//   - HotPath  → Chaindata, Tmp (low-latency NVMe)
//   - WarmPath → Snap, SnapDomain, SnapAccessors (NVMe recommended)
//   - ColdPath → SnapHistory, SnapIdx (HDD acceptable)
//   - DownloaderPath → Downloader (torrent data)
//
// Fallback rules:
//   - If warmPath is empty but hotPath is set, warmPath follows hotPath.
//   - If coldPath is empty, no override for cold dirs.
//   - If downloaderPath is empty but coldPath is set, downloaderPath follows coldPath.
func ApplyTierOverrides(dirs *Dirs, hotPath, warmPath, coldPath, downloaderPath string) {
	// --- Hot tier: chaindata + temp ---
	if hotPath != "" {
		dirs.Chaindata = filepath.Join(hotPath, "chaindata")
		dirs.Tmp = filepath.Join(hotPath, "temp")
	}

	// --- Warm tier: snapshot domain/accessor data ---
	// If warmPath is empty but hotPath is set, warm follows hot.
	effectiveWarm := warmPath
	if effectiveWarm == "" && hotPath != "" {
		effectiveWarm = hotPath
	}
	if effectiveWarm != "" {
		dirs.Snap = filepath.Join(effectiveWarm, "snapshots")
		dirs.SnapDomain = filepath.Join(effectiveWarm, "snapshots", "domain")
		dirs.SnapAccessors = filepath.Join(effectiveWarm, "snapshots", "accessor")
	}

	// --- Cold tier: history + indices ---
	if coldPath != "" {
		dirs.SnapHistory = filepath.Join(coldPath, "snapshots", "history")
		dirs.SnapIdx = filepath.Join(coldPath, "snapshots", "idx")
	}

	// --- Downloader ---
	// If downloaderPath is empty but coldPath is set, downloader follows cold.
	effectiveDL := downloaderPath
	if effectiveDL == "" && coldPath != "" {
		effectiveDL = coldPath
	}
	if effectiveDL != "" {
		dirs.Downloader = filepath.Join(effectiveDL, "downloader")
	}

	// Ensure all overridden directories exist.
	dir.MustExist(
		dirs.Chaindata, dirs.Tmp,
		dirs.Snap, dirs.SnapDomain, dirs.SnapAccessors,
		dirs.SnapHistory, dirs.SnapIdx,
		dirs.Downloader,
	)
}
