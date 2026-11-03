package mmap

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The mobile SDK (cmd/evmsdk) links this package and is built for the
// 32-bit Android ABIs too, so the package must compile for them.
func TestBuildsFor32BitTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	for _, arch := range []string{"arm", "386"} {
		cmd := exec.Command(gobin, "build", "-o", filepath.Join(t.TempDir(), "mmap.a"), ".")
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("GOARCH=%s: %v\n%s", arch, err, out)
		}
	}
}
