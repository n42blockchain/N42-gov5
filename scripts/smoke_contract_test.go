package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
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
	if !strings.Contains(release, "EEST result audit: \\`scripts/audit_eest_results.sh\\`") {
		t.Fatal("run_release_gate.sh missing EEST audit summary line")
	}
	if !strings.Contains(release, "run_step eest-audit") {
		t.Fatal("run_release_gate.sh missing eest-audit step")
	}
}

func TestMainWorkflowIncludesEESTResultsAudit(t *testing.T) {
	t.Parallel()

	workflow := readRepoFile(t, ".github", "workflows", "main.yml")
	for _, needle := range []string{
		"eest-results:",
		"name: EEST Results Audit",
		"bash ./scripts/audit_eest_results.sh | tee eest-audit.txt",
		"name: eest-audit-report",
	} {
		if !strings.Contains(workflow, needle) {
			t.Fatalf("main workflow missing %q", needle)
		}
	}
}

func TestStartHiveDevScriptAlwaysRebuildsHive(t *testing.T) {
	t.Parallel()

	content := readRepoFile(t, "scripts", "start_hive_dev_n42.sh")
	if !strings.Contains(content, `go build -o build/bin/hive ./hive.go`) {
		t.Fatal("start_hive_dev_n42.sh does not build the Hive binary")
	}
	if strings.Contains(content, `[ ! -x "$hive_dir/build/bin/hive" ]`) {
		t.Fatal("start_hive_dev_n42.sh can reuse a stale Hive binary")
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

func TestReleaseGateScriptProducesSummaryInStubMode(t *testing.T) {
	t.Parallel()

	resultRoot := t.TempDir()
	runID := "release-stub-pass"
	output, err := runReleaseGateScript(t, resultRoot, runID, "")
	if err != nil {
		t.Fatalf("run_release_gate.sh failed: %v\n%s", err, output)
	}

	if !strings.Contains(output, "summary=") {
		t.Fatalf("expected summary path in output, got:\n%s", output)
	}

	runDir := filepath.Join(resultRoot, runID)
	summary := readFile(t, filepath.Join(runDir, "summary.md"))
	for _, needle := range []string{
		"# N42 Release Check",
		"- EEST result audit: `scripts/audit_eest_results.sh`",
		"- Overall status: `PASS`",
		"| `maturity-baseline` | `PASS` |",
		"| `eest-audit` | `PASS` |",
		"| `interop-smoke` | `PASS` |",
		"| `soak-smoke` | `PASS` |",
	} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("summary missing %q:\n%s", needle, summary)
		}
	}
}

func TestReleaseGateScriptPropagatesStubFailureToSummary(t *testing.T) {
	t.Parallel()

	resultRoot := t.TempDir()
	runID := "release-stub-fail"
	output, err := runReleaseGateScript(t, resultRoot, runID, "eest-audit")
	if err == nil {
		t.Fatalf("expected run_release_gate.sh to fail, output:\n%s", output)
	}

	runDir := filepath.Join(resultRoot, runID)
	summary := readFile(t, filepath.Join(runDir, "summary.md"))
	for _, needle := range []string{
		"- Overall status: `FAIL`",
		"| `maturity-baseline` | `PASS` |",
		"| `eest-audit` | `FAIL` |",
		"| `ops-smoke` | `PASS` |",
		"| `interop-smoke` | `PASS` |",
		"| `soak-smoke` | `PASS` |",
	} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("summary missing %q:\n%s", needle, summary)
		}
	}
}

func TestEESTShardsScriptProducesSummaryInDryRun(t *testing.T) {
	t.Parallel()

	resultDir := filepath.Join(t.TempDir(), "eest-dry-run")
	output, err := runEESTShardsScript(t, resultDir, nil, "paris+shanghai", "prague")
	if err != nil {
		t.Fatalf("run_eest_shards.sh failed: %v\n%s", err, output)
	}

	if !strings.Contains(output, "results_dir=") {
		t.Fatalf("expected results_dir in output, got:\n%s", output)
	}

	summary := readFile(t, filepath.Join(resultDir, "summary.md"))
	for _, needle := range []string{
		"# EEST Shard Run Summary",
		"- Dry run: `1`",
		"- N42 revision: `",
		"- Hive revision: `",
		"- EEST revision: `",
		"- Status: `complete`",
		"| paris+shanghai | `.*/.*fork_(Paris\\|Shanghai)` | ~2,600 | `engine` | `0` |",
		"| prague | `.*/.*fork_Prague` | ~20,500 | `engine` | `0` |",
		"`paris+shanghai.log`",
		"`prague.log`",
	} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("summary missing %q:\n%s", needle, summary)
		}
	}
}

func TestEESTShardsScriptRetriesOnlyInfrastructureFailures(t *testing.T) {
	t.Parallel()

	resultDir := filepath.Join(t.TempDir(), "eest-infra-reruns")
	output, err := runEESTShardsScript(t, resultDir, nil, "paris+shanghai")
	if err != nil {
		t.Fatalf("run_eest_shards.sh failed: %v\n%s", err, output)
	}

	meta := readFile(t, filepath.Join(resultDir, "paris+shanghai.meta"))
	for _, needle := range []string{
		"infra_reruns=2",
		"infra_rerun_delay_seconds=1",
		"infra_rerun_match=ConnectionError|ConnectionRefusedError|RemoteDisconnected|ReadTimeout",
		"n42_revision=",
		"n42_dirty=",
		"hive_revision=",
		"hive_dirty=",
		"eest_revision=",
		"eest_dirty=",
		"--reruns 2",
		"--reruns-delay 1",
		"--only-rerun ConnectionError\\|ConnectionRefusedError\\|RemoteDisconnected\\|ReadTimeout",
	} {
		if !strings.Contains(meta, needle) {
			t.Fatalf("meta missing %q:\n%s", needle, meta)
		}
	}
}

func TestEESTShardsScriptCanDisableInfrastructureReruns(t *testing.T) {
	t.Parallel()

	resultDir := filepath.Join(t.TempDir(), "eest-no-infra-reruns")
	output, err := runEESTShardsScript(t, resultDir, []string{"EEST_INFRA_RERUNS=0"}, "paris+shanghai")
	if err != nil {
		t.Fatalf("run_eest_shards.sh failed: %v\n%s", err, output)
	}

	meta := readFile(t, filepath.Join(resultDir, "paris+shanghai.meta"))
	if !strings.Contains(meta, "infra_reruns=0") {
		t.Fatalf("meta missing disabled retry count:\n%s", meta)
	}
	if strings.Contains(meta, "--reruns ") || strings.Contains(meta, "--only-rerun ") {
		t.Fatalf("disabled retry arguments unexpectedly present:\n%s", meta)
	}
}

func TestEESTShardsScriptWritesPartialSummaryOnTermination(t *testing.T) {
	t.Parallel()

	resultDir := filepath.Join(t.TempDir(), "eest-terminated")
	scriptPath := filepath.Join(repoRoot(t), "scripts", "run_eest_shards.sh")
	cmd := exec.Command("bash", scriptPath, "paris+shanghai", "cancun")
	cmd.Env = append(os.Environ(),
		"EEST_DRY_RUN=1",
		"EEST_RESULTS_DIR="+resultDir,
		"EEST_TEST_RUN_SHARD_DELAY=5",
		"EEST_SHARD_JOBS=1",
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		t.Fatalf("start run_eest_shards.sh: %v", err)
	}

	time.Sleep(1 * time.Second)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal run_eest_shards.sh: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("expected run_eest_shards.sh to terminate with error, output:\n%s", output.String())
	}

	summary := readFile(t, filepath.Join(resultDir, "summary.md"))
	for _, needle := range []string{
		"# EEST Shard Run Summary",
		"- Status: `partial`",
		"| paris+shanghai | `.*/.*fork_(Paris\\|Shanghai)` | ~2,600 | `engine` | `incomplete` | `-` | `paris+shanghai.log` |",
		"| cancun | `.*/.*fork_Cancun` | ~17,250 | `engine` | `incomplete` | `-` | `cancun.log` |",
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

func runReleaseGateScript(t *testing.T, resultRoot, runID, failStep string) (string, error) {
	t.Helper()

	scriptPath := filepath.Join(repoRoot(t), "scripts", "run_release_gate.sh")
	cmd := exec.Command("bash", scriptPath, "--result-dir", resultRoot)
	cmd.Env = append(os.Environ(),
		"RELEASE_GATE_STUB=1",
		"RELEASE_RUN_ID="+runID,
	)
	if failStep != "" {
		cmd.Env = append(cmd.Env, "RELEASE_GATE_STUB_FAIL_STEP="+failStep)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func runEESTShardsScript(t *testing.T, resultDir string, extraEnv []string, shards ...string) (string, error) {
	t.Helper()

	scriptPath := filepath.Join(repoRoot(t), "scripts", "run_eest_shards.sh")
	cmd := exec.Command("bash", append([]string{scriptPath}, shards...)...)
	cmd.Env = append(os.Environ(),
		"EEST_DRY_RUN=1",
		"EEST_RESULTS_DIR="+resultDir,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
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
