package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEESTAuditScriptPassesForCompleteRuns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "20260415-complete")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	writeTestFile(t, filepath.Join(runDir, "summary.md"), "# EEST Shard Run Summary\n\n- Status: `complete`\n")
	writeTestFile(t, filepath.Join(runDir, "prague.meta"), strings.Join([]string{
		"shard=prague",
		"rc=0",
		"duration_seconds=123",
	}, "\n")+"\n")
	writeTestFile(t, filepath.Join(runDir, "prague.log"), "===================== 20878 passed in 22902.28s (6:21:42) =====================\n")

	output, err := runEESTAuditScript(t, root)
	if err != nil {
		t.Fatalf("audit_eest_results.sh failed: %v\n%s", err, output)
	}

	for _, needle := range []string{
		"# EEST Result Audit",
		"| `20260415-complete` | `PASS` | `1` | none |",
		"- Runs scanned: `1`",
		"- Passing runs: `1`",
		"- Failing runs: `0`",
		"- Status: `PASS`",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output missing %q:\n%s", needle, output)
		}
	}
}

func TestEESTAuditScriptRejectsFailedAndPartialResults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "20260416-failed")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	writeTestFile(t, filepath.Join(runDir, "summary.md"), "# EEST Shard Run Summary\n\n- Status: `partial`\n")
	writeTestFile(t, filepath.Join(runDir, "cancun.meta"), strings.Join([]string{
		"shard=cancun",
		"rc=1",
		"duration_seconds=not-a-number",
	}, "\n")+"\n")
	writeTestFile(t, filepath.Join(runDir, "cancun.log"), "1 failed, 17782 passed\n")

	output, err := runEESTAuditScript(t, root)
	if err == nil {
		t.Fatalf("expected audit_eest_results.sh to fail, output:\n%s", output)
	}

	for _, needle := range []string{
		"| `20260416-failed` | `FAIL` | `1` |",
		"summary.md is not complete",
		"cancun.meta has rc=1",
		"cancun.meta has invalid duration_seconds=not-a-number",
		"- Status: `FAIL`",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output missing %q:\n%s", needle, output)
		}
	}
}

func TestEESTAuditScriptFailsForIncompleteRuns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "20260413-postpush-osaka")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	writeTestFile(t, filepath.Join(runDir, "osaka.meta"), strings.Join([]string{
		"shard=osaka",
		"selector=.*/.*fork_Osaka",
	}, "\n")+"\n")
	writeTestFile(t, filepath.Join(runDir, "osaka.log"), "................................................................. [20930/21583]\n")

	output, err := runEESTAuditScript(t, root)
	if err == nil {
		t.Fatalf("expected audit_eest_results.sh to fail, output:\n%s", output)
	}

	for _, needle := range []string{
		"# EEST Result Audit",
		"| `20260413-postpush-osaka` | `FAIL` | `1` |",
		"missing summary.md",
		"osaka.meta missing rc",
		"osaka.meta missing duration_seconds",
		"- Failing runs: `1`",
		"- Status: `FAIL`",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output missing %q:\n%s", needle, output)
		}
	}
}

func TestEESTAuditScriptSkipsIgnoredRuns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "20260316-empty")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	writeTestFile(t, filepath.Join(runDir, ".eest-audit-ignore"), "empty historical result directory\n")

	output, err := runEESTAuditScript(t, root)
	if err != nil {
		t.Fatalf("audit_eest_results.sh failed: %v\n%s", err, output)
	}

	for _, needle := range []string{
		"| `20260316-empty` | `SKIP` | `0` | empty historical result directory |",
		"- Passing runs: `0`",
		"- Failing runs: `0`",
		"- Skipped runs: `1`",
		"- Status: `PASS`",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output missing %q:\n%s", needle, output)
		}
	}
}

func TestEESTAuditScriptCanFailOnIgnoredRuns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "20260316-empty")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	writeTestFile(t, filepath.Join(runDir, ".eest-audit-ignore"), "empty historical result directory\n")

	output, err := runEESTAuditScript(t, root, "--fail-on-skip")
	if err == nil {
		t.Fatalf("expected audit_eest_results.sh to fail in fail-on-skip mode, output:\n%s", output)
	}

	for _, needle := range []string{
		"- Fail on skip: `1`",
		"| `20260316-empty` | `SKIP` | `0` | empty historical result directory |",
		"- Failing runs: `0`",
		"- Skipped runs: `1`",
		"- Status: `FAIL`",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output missing %q:\n%s", needle, output)
		}
	}
}

func runEESTAuditScript(t *testing.T, root string, extraArgs ...string) (string, error) {
	t.Helper()

	scriptPath := filepath.Join(repoRoot(t), "scripts", "audit_eest_results.sh")
	args := append([]string{scriptPath, "--root", root}, extraArgs...)
	cmd := exec.Command("bash", args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
