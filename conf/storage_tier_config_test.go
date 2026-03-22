// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package conf

import (
	"path/filepath"
	"testing"
)

func TestStorageTierValidate_Disabled(t *testing.T) {
	cfg := DefaultStorageTierCfg()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled config should validate: %v", err)
	}
}

func TestStorageTierValidate_ValidPaths(t *testing.T) {
	tmp := t.TempDir()
	cfg := StorageTierCfg{
		Enabled: true,
		HotPath: filepath.Join(tmp, "hot"),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config should pass: %v", err)
	}
}

func TestStorageTierValidate_AllPaths(t *testing.T) {
	tmp := t.TempDir()
	cfg := StorageTierCfg{
		Enabled:        true,
		HotPath:        filepath.Join(tmp, "hot"),
		WarmPath:       filepath.Join(tmp, "warm"),
		ColdPath:       filepath.Join(tmp, "cold"),
		DownloaderPath: filepath.Join(tmp, "dl"),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("all-paths config should pass: %v", err)
	}
}

func TestStorageTierValidate_EnabledNoPaths(t *testing.T) {
	cfg := StorageTierCfg{Enabled: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabled with no paths should fail validation")
	}
}

func TestStorageTierValidate_InvalidPath(t *testing.T) {
	cfg := StorageTierCfg{
		Enabled: true,
		HotPath: string([]byte{0}), // null byte in path
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid path should fail validation")
	}
}
