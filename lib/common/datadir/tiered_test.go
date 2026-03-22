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
	"testing"
)

func TestApplyTierOverrides_AllPaths(t *testing.T) {
	base := t.TempDir()
	dirs := New(base)

	hot := filepath.Join(t.TempDir(), "nvme")
	warm := filepath.Join(t.TempDir(), "nvme2")
	cold := filepath.Join(t.TempDir(), "hdd")
	dl := filepath.Join(t.TempDir(), "hdd2")

	ApplyTierOverrides(&dirs, hot, warm, cold, dl)

	// Hot tier
	if dirs.Chaindata != filepath.Join(hot, "chaindata") {
		t.Errorf("Chaindata = %s, want %s", dirs.Chaindata, filepath.Join(hot, "chaindata"))
	}
	if dirs.Tmp != filepath.Join(hot, "temp") {
		t.Errorf("Tmp = %s, want %s", dirs.Tmp, filepath.Join(hot, "temp"))
	}

	// Warm tier
	if dirs.Snap != filepath.Join(warm, "snapshots") {
		t.Errorf("Snap = %s, want %s", dirs.Snap, filepath.Join(warm, "snapshots"))
	}
	if dirs.SnapDomain != filepath.Join(warm, "snapshots", "domain") {
		t.Errorf("SnapDomain = %s, want %s", dirs.SnapDomain, filepath.Join(warm, "snapshots", "domain"))
	}
	if dirs.SnapAccessors != filepath.Join(warm, "snapshots", "accessor") {
		t.Errorf("SnapAccessors = %s, want %s", dirs.SnapAccessors, filepath.Join(warm, "snapshots", "accessor"))
	}

	// Cold tier
	if dirs.SnapHistory != filepath.Join(cold, "snapshots", "history") {
		t.Errorf("SnapHistory = %s, want %s", dirs.SnapHistory, filepath.Join(cold, "snapshots", "history"))
	}
	if dirs.SnapIdx != filepath.Join(cold, "snapshots", "idx") {
		t.Errorf("SnapIdx = %s, want %s", dirs.SnapIdx, filepath.Join(cold, "snapshots", "idx"))
	}

	// Downloader
	if dirs.Downloader != filepath.Join(dl, "downloader") {
		t.Errorf("Downloader = %s, want %s", dirs.Downloader, filepath.Join(dl, "downloader"))
	}

	// Unchanged fields
	if dirs.DataDir != base {
		t.Errorf("DataDir should be unchanged, got %s", dirs.DataDir)
	}
	if dirs.TxPool != filepath.Join(base, "txpool") {
		t.Errorf("TxPool should be unchanged, got %s", dirs.TxPool)
	}
}

func TestApplyTierOverrides_HotOnly(t *testing.T) {
	base := t.TempDir()
	dirs := New(base)

	hot := filepath.Join(t.TempDir(), "nvme")

	ApplyTierOverrides(&dirs, hot, "", "", "")

	// Hot tier applied
	if dirs.Chaindata != filepath.Join(hot, "chaindata") {
		t.Errorf("Chaindata = %s, want %s", dirs.Chaindata, filepath.Join(hot, "chaindata"))
	}
	if dirs.Tmp != filepath.Join(hot, "temp") {
		t.Errorf("Tmp = %s, want %s", dirs.Tmp, filepath.Join(hot, "temp"))
	}

	// Warm follows hot when warm is empty
	if dirs.Snap != filepath.Join(hot, "snapshots") {
		t.Errorf("Snap = %s, want warm to follow hot: %s", dirs.Snap, filepath.Join(hot, "snapshots"))
	}
	if dirs.SnapDomain != filepath.Join(hot, "snapshots", "domain") {
		t.Errorf("SnapDomain = %s, want warm to follow hot: %s", dirs.SnapDomain, filepath.Join(hot, "snapshots", "domain"))
	}

	// Cold unchanged (no cold path)
	if dirs.SnapHistory != filepath.Join(base, "snapshots", "history") {
		t.Errorf("SnapHistory should be unchanged, got %s", dirs.SnapHistory)
	}
	if dirs.SnapIdx != filepath.Join(base, "snapshots", "idx") {
		t.Errorf("SnapIdx should be unchanged, got %s", dirs.SnapIdx)
	}

	// Downloader unchanged (no cold or dl path)
	if dirs.Downloader != filepath.Join(base, "downloader") {
		t.Errorf("Downloader should be unchanged, got %s", dirs.Downloader)
	}
}

func TestApplyTierOverrides_ColdOnly(t *testing.T) {
	base := t.TempDir()
	dirs := New(base)

	cold := filepath.Join(t.TempDir(), "hdd")

	ApplyTierOverrides(&dirs, "", "", cold, "")

	// Hot unchanged
	if dirs.Chaindata != filepath.Join(base, "chaindata") {
		t.Errorf("Chaindata should be unchanged, got %s", dirs.Chaindata)
	}

	// Warm unchanged (no hot or warm path)
	if dirs.Snap != filepath.Join(base, "snapshots") {
		t.Errorf("Snap should be unchanged, got %s", dirs.Snap)
	}

	// Cold applied
	if dirs.SnapHistory != filepath.Join(cold, "snapshots", "history") {
		t.Errorf("SnapHistory = %s, want %s", dirs.SnapHistory, filepath.Join(cold, "snapshots", "history"))
	}
	if dirs.SnapIdx != filepath.Join(cold, "snapshots", "idx") {
		t.Errorf("SnapIdx = %s, want %s", dirs.SnapIdx, filepath.Join(cold, "snapshots", "idx"))
	}

	// Downloader follows cold
	if dirs.Downloader != filepath.Join(cold, "downloader") {
		t.Errorf("Downloader = %s, want cold fallback: %s", dirs.Downloader, filepath.Join(cold, "downloader"))
	}
}

func TestApplyTierOverrides_Empty(t *testing.T) {
	base := t.TempDir()
	dirs := New(base)

	// Save original values
	origChaindata := dirs.Chaindata
	origTmp := dirs.Tmp
	origSnap := dirs.Snap
	origSnapDomain := dirs.SnapDomain
	origSnapAccessors := dirs.SnapAccessors
	origSnapHistory := dirs.SnapHistory
	origSnapIdx := dirs.SnapIdx
	origDownloader := dirs.Downloader

	ApplyTierOverrides(&dirs, "", "", "", "")

	if dirs.Chaindata != origChaindata {
		t.Errorf("Chaindata changed: %s -> %s", origChaindata, dirs.Chaindata)
	}
	if dirs.Tmp != origTmp {
		t.Errorf("Tmp changed: %s -> %s", origTmp, dirs.Tmp)
	}
	if dirs.Snap != origSnap {
		t.Errorf("Snap changed: %s -> %s", origSnap, dirs.Snap)
	}
	if dirs.SnapDomain != origSnapDomain {
		t.Errorf("SnapDomain changed: %s -> %s", origSnapDomain, dirs.SnapDomain)
	}
	if dirs.SnapAccessors != origSnapAccessors {
		t.Errorf("SnapAccessors changed: %s -> %s", origSnapAccessors, dirs.SnapAccessors)
	}
	if dirs.SnapHistory != origSnapHistory {
		t.Errorf("SnapHistory changed: %s -> %s", origSnapHistory, dirs.SnapHistory)
	}
	if dirs.SnapIdx != origSnapIdx {
		t.Errorf("SnapIdx changed: %s -> %s", origSnapIdx, dirs.SnapIdx)
	}
	if dirs.Downloader != origDownloader {
		t.Errorf("Downloader changed: %s -> %s", origDownloader, dirs.Downloader)
	}
}
