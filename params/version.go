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

package params

import (
	"fmt"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/modules"
)

var (
	// Following vars are injected through the build flags (see Makefile)
	GitCommit string
	GitBranch string
	GitTag    string
)

// Version format: Major.Minor.Build
// - Major: Annual release (5, 6, 7...)
// - Minor: Feature release within year (5.1, 5.2...)
// - Build: Auto-incremented on each build (486, 487, 488...)
const (
	VersionMajor       = 5   // Major version - annual release
	VersionMinor       = 1   // Minor version - feature release
	VersionBuild       = 487 // Build number - auto-incremented
	VersionModifier    = ""  // Modifier component (alpha, beta, stable)
	VersionKeyCreated  = "n42VersionCreated"
	VersionKeyFinished = "n42VersionFinished"
)

func withModifier(vsn string) string {
	if !isStable() {
		vsn += "-" + VersionModifier
	}
	return vsn
}

func isStable() bool {
	return VersionModifier == "stable"
}

func isRelease() bool {
	return isStable() || VersionModifier == "alpha" || VersionModifier == "beta"
}

// Version holds the textual version string.
var Version = func() string {
	return fmt.Sprintf("%d.%d.%d", VersionMajor, VersionMinor, VersionBuild)
}()

// VersionWithMeta holds the textual version string including the metadata.
var VersionWithMeta = func() string {
	v := Version
	if VersionModifier != "" {
		v += "-" + VersionModifier
	}
	return v
}()

// ArchiveVersion holds the textual version string used for Geth archives.
// e.g. "1.8.11-dea1ce05" for stable releases, or
//
//	"1.8.13-unstable-21c059b6" for unstable releases
func ArchiveVersion(gitCommit string) string {
	vsn := withModifier(Version)
	if len(gitCommit) >= 8 {
		vsn += "-" + gitCommit[:8]
	}
	return vsn
}

func VersionWithCommit(gitCommit, gitDate string) string {
	vsn := VersionWithMeta
	if len(gitCommit) >= 8 {
		vsn += "-" + gitCommit[:8]
	}
	return vsn
}

func SetN42Version(tx kv.RwTx, versionKey string) error {
	versionKeyByte := []byte(versionKey)
	hasVersion, err := tx.Has(modules.DatabaseInfo, versionKeyByte)
	if err != nil {
		return err
	}
	if hasVersion {
		return nil
	}
	// Save version if it does not exist
	if err := tx.Put(modules.DatabaseInfo, versionKeyByte, []byte(Version)); err != nil {
		return err
	}
	return nil
}
