// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package params

import (
	"fmt"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestVersionString(t *testing.T) {
	want := fmt.Sprintf("%d.%d.%d", VersionMajor, VersionMinor, VersionBuild)
	if Version != want {
		t.Fatalf("Version = %q, want %q", Version, want)
	}
}

func TestWithModifierCurrentBuild(t *testing.T) {
	// VersionModifier is a compile-time constant in this build; exercise
	// withModifier against whatever it currently is and check the
	// documented contract: it appends "-"+modifier unless the modifier is
	// "stable" or empty.
	got := withModifier("1.2.3")
	if VersionModifier == "" || VersionModifier == "stable" {
		if got != "1.2.3" {
			t.Fatalf("withModifier = %q, want %q", got, "1.2.3")
		}
	} else if got != "1.2.3-"+VersionModifier {
		t.Fatalf("withModifier = %q, want %q", got, "1.2.3-"+VersionModifier)
	}
}

func TestIsStable(t *testing.T) {
	want := VersionModifier == "stable"
	if isStable() != want {
		t.Fatalf("isStable() = %v, want %v", isStable(), want)
	}
}

func TestArchiveVersion(t *testing.T) {
	short := ArchiveVersion("abc")
	if !strings.HasPrefix(short, Version) || strings.Contains(short, "-abc") {
		t.Fatalf("short commit hash should not be appended: %q", short)
	}

	full := ArchiveVersion("deadbeefcafebabe")
	if !strings.HasSuffix(full, "-deadbeef") {
		t.Fatalf("ArchiveVersion should append 8-char commit: %q", full)
	}
}

func TestVersionWithCommit(t *testing.T) {
	got := VersionWithCommit("deadbeefcafebabe", "2024-01-01")
	if !strings.HasPrefix(got, VersionWithMeta) {
		t.Fatalf("VersionWithCommit should prefix VersionWithMeta: %q", got)
	}
	if !strings.HasSuffix(got, "-deadbeef") {
		t.Fatalf("VersionWithCommit should append 8-char commit: %q", got)
	}

	short := VersionWithCommit("ab", "2024-01-01")
	if short != VersionWithMeta {
		t.Fatalf("short commit should not be appended: %q", short)
	}
}

func TestSetN42Version(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	const key = "testVersionKey"
	if err := SetN42Version(tx, key); err != nil {
		t.Fatalf("SetN42Version first call failed: %v", err)
	}

	got, err := tx.GetOne(modules.DatabaseInfo, []byte(key))
	if err != nil {
		t.Fatalf("GetOne failed: %v", err)
	}
	if string(got) != Version {
		t.Fatalf("stored version = %q, want %q", got, Version)
	}

	// Second call must be a no-op (idempotent) and not error.
	if err := SetN42Version(tx, key); err != nil {
		t.Fatalf("SetN42Version second call failed: %v", err)
	}
}
