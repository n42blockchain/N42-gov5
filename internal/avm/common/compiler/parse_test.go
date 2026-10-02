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

package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercises JSON parsing, path handling and argument assembly without
// invoking the external solc/vyper binaries, which are not available in
// the test environment.

func TestParseCombinedJSONLegacy(t *testing.T) {
	raw := `{
		"contracts": {
			"test.sol:test": {
				"bin": "6060",
				"bin-runtime": "6061",
				"srcmap": "1:2:3",
				"srcmap-runtime": "4:5:6",
				"abi": "[{\"name\":\"multiply\"}]",
				"devdoc": "{\"methods\":{}}",
				"userdoc": "{\"methods\":{}}",
				"hashes": {"multiply(uint256)": "c6888fa1"}
			}
		},
		"version": "0.4.25+commit.abc"
	}`

	contracts, err := ParseCombinedJSON([]byte(raw), "source code", "0.4.25", "0.4.25", "--combined-json bin")
	if err != nil {
		t.Fatalf("ParseCombinedJSON returned error: %v", err)
	}
	c, ok := contracts["test.sol:test"]
	if !ok {
		t.Fatalf("expected contract test.sol:test, got %#v", contracts)
	}
	if c.Code != "0x6060" {
		t.Errorf("Code = %q, want 0x6060", c.Code)
	}
	if c.RuntimeCode != "0x6061" {
		t.Errorf("RuntimeCode = %q, want 0x6061", c.RuntimeCode)
	}
	if c.Hashes["multiply(uint256)"] != "c6888fa1" {
		t.Errorf("Hashes missing expected selector: %#v", c.Hashes)
	}
	if c.Info.Language != "Solidity" || c.Info.LanguageVersion != "0.4.25" {
		t.Errorf("unexpected Info: %#v", c.Info)
	}
	if c.Info.Source != "source code" {
		t.Errorf("Source = %q", c.Info.Source)
	}
}

func TestParseCombinedJSONLegacyBadAbi(t *testing.T) {
	raw := `{"contracts":{"a":{"abi":"not-json"}},"version":"0.4.25"}`
	if _, err := ParseCombinedJSON([]byte(raw), "", "", "", ""); err == nil {
		t.Fatal("expected error for malformed abi field")
	}
}

func TestParseCombinedJSONV8(t *testing.T) {
	// v0.8 serializes abi/userdoc/devdoc as embedded JSON objects rather
	// than JSON-encoded strings, which fails legacy unmarshal and falls
	// through to parseCombinedJSONV8.
	raw := `{
		"contracts": {
			"test.sol:test": {
				"bin": "6060",
				"bin-runtime": "6061",
				"abi": [{"name":"multiply"}],
				"devdoc": {"methods":{}},
				"userdoc": {"methods":{}},
				"metadata": "{}",
				"hashes": {"multiply(uint256)": "c6888fa1"}
			}
		},
		"version": "0.8.17+commit.abc"
	}`

	contracts, err := ParseCombinedJSON([]byte(raw), "src", "0.8.17", "0.8.17", "opts")
	if err != nil {
		t.Fatalf("ParseCombinedJSON (v8) returned error: %v", err)
	}
	c, ok := contracts["test.sol:test"]
	if !ok {
		t.Fatalf("expected contract, got %#v", contracts)
	}
	if c.Code != "0x6060" || c.RuntimeCode != "0x6061" {
		t.Errorf("unexpected code fields: %#v", c)
	}
	abiList, ok := c.Info.AbiDefinition.([]interface{})
	if !ok || len(abiList) != 1 {
		t.Errorf("unexpected AbiDefinition: %#v", c.Info.AbiDefinition)
	}
}

func TestParseCombinedJSONInvalid(t *testing.T) {
	if _, err := ParseCombinedJSON([]byte("not json at all"), "", "", "", ""); err == nil {
		t.Fatal("expected error for invalid JSON on both legacy and v8 paths")
	}
}

func TestParseVyperJSON(t *testing.T) {
	raw := `{
		"version": "0.3.7",
		"test.vy": {
			"bytecode": "0x6060",
			"bytecode_runtime": "0x6061",
			"abi": [{"name":"foo"}],
			"source_map": {"foo":"bar"}
		}
	}`
	contracts, err := ParseVyperJSON([]byte(raw), "source", "0.3.7", "0.3.7", "-f combined_json")
	if err != nil {
		t.Fatalf("ParseVyperJSON returned error: %v", err)
	}
	c, ok := contracts["test.vy"]
	if !ok {
		t.Fatalf("expected contract test.vy, got %#v", contracts)
	}
	if c.Code != "0x6060" || c.RuntimeCode != "0x6061" {
		t.Errorf("unexpected contract fields: %#v", c)
	}
	if c.Info.Language != "Vyper" {
		t.Errorf("Language = %q, want Vyper", c.Info.Language)
	}
	if _, present := contracts["version"]; present {
		t.Errorf("the 'version' key must be skipped as a contract entry")
	}
}

func TestParseVyperJSONInvalidContract(t *testing.T) {
	raw := `{"version":"0.3.7","test.vy":"not-an-object"}`
	if _, err := ParseVyperJSON([]byte(raw), "", "", "", ""); err == nil {
		t.Fatal("expected error for non-object contract entry")
	}
}

func TestParseVyperJSONInvalidJSON(t *testing.T) {
	if _, err := ParseVyperJSON([]byte("{not json"), "", "", "", ""); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestSolidityAllowedPaths(t *testing.T) {
	s := &Solidity{}
	paths := s.allowedPaths()
	if !strings.Contains(paths, ".") || !strings.Contains(paths, "../") {
		t.Errorf("allowedPaths missing defaults: %q", paths)
	}

	s.ExtraAllowedPath = []string{"/extra/one", "/extra/two"}
	paths = s.allowedPaths()
	if !strings.Contains(paths, "/extra/one") || !strings.Contains(paths, "/extra/two") {
		t.Errorf("allowedPaths missing extra paths: %q", paths)
	}
}

func TestSolidityMakeArgs(t *testing.T) {
	old := &Solidity{Major: 0, Minor: 4, Patch: 6}
	args := old.makeArgs()
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "metadata") {
		t.Errorf("old solc should not request metadata,hashes: %q", joined)
	}

	newer := &Solidity{Major: 0, Minor: 4, Patch: 7}
	args = newer.makeArgs()
	joined = strings.Join(args, " ")
	if !strings.Contains(joined, "metadata") || !strings.Contains(joined, "hashes") {
		t.Errorf("newer solc should request metadata,hashes: %q", joined)
	}

	major := &Solidity{Major: 1}
	joined = strings.Join(major.makeArgs(), " ")
	if !strings.Contains(joined, "metadata") {
		t.Errorf("major>0 solc should request metadata,hashes: %q", joined)
	}
}

func TestVyperMakeArgs(t *testing.T) {
	v := &Vyper{}
	args := v.makeArgs()
	if len(args) != 2 || args[0] != "-f" || args[1] != "combined_json" {
		t.Errorf("unexpected vyper args: %#v", args)
	}
}

func TestSlurpFiles(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.sol")
	f2 := filepath.Join(dir, "b.sol")
	if err := os.WriteFile(f1, []byte("contract A {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("contract B {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := slurpFiles([]string{f1, f2})
	if err != nil {
		t.Fatalf("slurpFiles returned error: %v", err)
	}
	want := "contract A {}contract B {}"
	if got != want {
		t.Errorf("slurpFiles = %q, want %q", got, want)
	}
}

func TestSlurpFilesMissing(t *testing.T) {
	if _, err := slurpFiles([]string{filepath.Join(t.TempDir(), "missing.sol")}); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestVersionRegexp(t *testing.T) {
	m := versionRegexp.FindStringSubmatch("solc, the solidity compiler commandline interface\nVersion: 0.8.17+commit.8df45f5f.Linux.g++")
	if len(m) != 4 || m[1] != "0" || m[2] != "8" || m[3] != "17" {
		t.Errorf("versionRegexp parsed = %#v", m)
	}
}

func TestSolidityVersionMissingBinary(t *testing.T) {
	if _, err := SolidityVersion("n42-nonexistent-solc-binary"); err == nil {
		t.Fatal("expected error when solc binary is missing")
	}
}

func TestVyperVersionMissingBinary(t *testing.T) {
	if _, err := VyperVersion("n42-nonexistent-vyper-binary"); err == nil {
		t.Fatal("expected error when vyper binary is missing")
	}
}

func TestCompileSolidityNoSourceFiles(t *testing.T) {
	if _, err := CompileSolidity("solc"); err == nil {
		t.Fatal("expected error when no source files are given")
	}
}

func TestCompileSolidityStringEmptySource(t *testing.T) {
	if _, err := CompileSolidityString("solc", ""); err == nil {
		t.Fatal("expected error for empty source string")
	}
}

func TestCompileVyperNoSourceFiles(t *testing.T) {
	if _, err := CompileVyper("vyper"); err == nil {
		t.Fatal("expected error when no source files are given")
	}
}
