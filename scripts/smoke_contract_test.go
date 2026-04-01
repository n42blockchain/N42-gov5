package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInteropSmokeScriptPinsEthdevMode(t *testing.T) {
	t.Parallel()

	content := readRepoFile(t, "scripts", "run_interop_smoke.sh")
	for _, needle := range []string{
		`node_mode_flag="--ethdev"`,
		`smoke_start_ethdev_node`,
		"Node mode: \\`$node_mode_flag\\`",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("run_interop_smoke.sh missing %q", needle)
		}
	}
}

func TestMaturityAndInteropMakeTargetsDocumentExecutionModel(t *testing.T) {
	t.Parallel()

	content := readRepoFile(t, "Makefile")
	for _, needle := range []string{
		`run_maturity_baseline.sh (package-test baseline; no node boot)`,
		"maturity-baseline - smoke + build/vet/test/lint/race-core 基线记录（不启动节点）",
		`run_interop_smoke.sh (ephemeral --ethdev node)`,
		"interop-smoke     - RPC/Blockscout/Hive/EEST interop gate（临时 --ethdev 节点）",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("Makefile missing %q", needle)
		}
	}
}

func TestReleaseAndMaturitySummariesExplainExecutionModel(t *testing.T) {
	t.Parallel()

	maturity := readRepoFile(t, "scripts", "run_maturity_baseline.sh")
	if !strings.Contains(maturity, "Execution model: \\`$execution_model\\`") {
		t.Fatal("run_maturity_baseline.sh missing execution model summary line")
	}

	release := readRepoFile(t, "scripts", "run_release_gate.sh")
	if !strings.Contains(release, "Interop node mode: \\`--ethdev\\`") {
		t.Fatal("run_release_gate.sh missing interop node mode summary line")
	}
}

func TestInteropSmokeScriptProducesSummaryInStubMode(t *testing.T) {
	t.Parallel()

	resultRoot := t.TempDir()
	runID := "stub-pass"
	output, err := runInteropSmokeScript(t, resultRoot, runID, "")
	if err != nil {
		t.Fatalf("run_interop_smoke.sh failed: %v\n%s", err, output)
	}

	if !strings.Contains(output, "summary=") {
		t.Fatalf("expected summary path in output, got:\n%s", output)
	}

	runDir := filepath.Join(resultRoot, runID)
	summary := readFile(t, filepath.Join(runDir, "summary.md"))
	for _, needle := range []string{
		"# N42 Interop Smoke",
		"- Node mode: `--ethdev`",
		"- Overall status: `PASS`",
		"| `build-node` | `PASS` |",
		"| `hive-engine-auth` | `PASS` |",
		"| `eest-collect` | `PASS` |",
	} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("summary missing %q:\n%s", needle, summary)
		}
	}

	for _, rel := range []string{
		"hive-auth-results",
		"eest-collect",
		"rows.md",
		"summary.md",
	} {
		if _, err := os.Stat(filepath.Join(runDir, rel)); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}
}

func TestInteropSmokeScriptPropagatesStubFailureToSummary(t *testing.T) {
	t.Parallel()

	resultRoot := t.TempDir()
	runID := "stub-fail"
	output, err := runInteropSmokeScript(t, resultRoot, runID, "hive-engine-auth")
	if err == nil {
		t.Fatalf("expected run_interop_smoke.sh to fail, output:\n%s", output)
	}

	runDir := filepath.Join(resultRoot, runID)
	summary := readFile(t, filepath.Join(runDir, "summary.md"))
	for _, needle := range []string{
		"- Overall status: `FAIL`",
		"| `hive-engine-auth` | `FAIL` |",
		"| `shutdown` | `PASS` |",
	} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("summary missing %q:\n%s", needle, summary)
		}
	}
}

func readRepoFile(t *testing.T, elems ...string) string {
	t.Helper()

	repoRoot := repoRoot(t)
	path := filepath.Join(append([]string{repoRoot}, elems...)...)
	return readFile(t, path)
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func runInteropSmokeScript(t *testing.T, resultRoot, runID, failStep string) (string, error) {
	t.Helper()

	scriptPath := filepath.Join(repoRoot(t), "scripts", "run_interop_smoke.sh")
	cmd := exec.Command("bash", scriptPath, "--result-dir", resultRoot)
	cmd.Env = append(os.Environ(),
		"INTEROP_SMOKE_STUB=1",
		"INTEROP_RUN_ID="+runID,
	)
	if failStep != "" {
		cmd.Env = append(cmd.Env, "INTEROP_SMOKE_STUB_FAIL_STEP="+failStep)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Dir(filepath.Dir(testFile))
}
