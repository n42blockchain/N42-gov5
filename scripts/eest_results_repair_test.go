package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEESTRepairScriptBackfillsSummaryAndMeta(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "20260413-postpush-prague")
	mustMkdirAll(t, runDir)

	writeTestFile(t, filepath.Join(runDir, "prague.meta"), strings.Join([]string{
		"shard=prague",
		"selector=.*/.*fork_Prague",
		"target=~20,500",
		"mode=consume-engine",
		"python=3.13",
		"pytest_workers=3",
	}, "\n")+"\n")
	writeTestFile(t, filepath.Join(runDir, "prague.log"), "................................................................. [19240/20878]\n")

	output, err := runEESTRepairScript(t, root)
	if err != nil {
		t.Fatalf("repair_eest_results.sh failed: %v\n%s", err, output)
	}

	meta := readFile(t, filepath.Join(runDir, "prague.meta"))
	for _, needle := range []string{
		"rc=incomplete",
		"duration_seconds=-",
	} {
		if !strings.Contains(meta, needle) {
			t.Fatalf("meta missing %q:\n%s", needle, meta)
		}
	}

	summary := readFile(t, filepath.Join(runDir, "summary.md"))
	for _, needle := range []string{
		"# EEST Shard Run Summary",
		"- Generated: `20260413-postpush-prague`",
		"- Status: `partial`",
		"| prague | `.*/.*fork_Prague` | ~20,500 | `incomplete` | `-` | `prague.log` |",
	} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("summary missing %q:\n%s", needle, summary)
		}
	}
}

func TestEESTRepairScriptMarksEmptyRunIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "20260316-empty")
	mustMkdirAll(t, runDir)

	output, err := runEESTRepairScript(t, root)
	if err != nil {
		t.Fatalf("repair_eest_results.sh failed: %v\n%s", err, output)
	}

	ignoreText := readFile(t, filepath.Join(runDir, ".eest-audit-ignore"))
	if !strings.Contains(ignoreText, "empty historical result directory") {
		t.Fatalf("ignore marker not written:\n%s", ignoreText)
	}
	if !strings.Contains(output, "| `20260316-empty` | `ignore` | marked empty directory as abandoned |") {
		t.Fatalf("repair output missing ignore action:\n%s", output)
	}
}

func runEESTRepairScript(t *testing.T, root string) (string, error) {
	t.Helper()

	scriptPath := filepath.Join(repoRoot(t), "scripts", "repair_eest_results.sh")
	cmd := exec.Command("bash", scriptPath, "--root", root)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
