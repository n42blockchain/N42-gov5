package hotstuff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func deterministicVoteSetup(t *testing.T) *testSetup {
	setup := newTestSetup(t, 4)
	for i := range setup.keys {
		scalar := make([]byte, 32)
		scalar[31] = byte(i + 1)
		key, err := bls.SecretKeyFromBytes(scalar)
		if err != nil {
			t.Fatal(err)
		}
		setup.keys[i] = key
		setup.pubKeys[i] = key.PublicKey()
		setup.validators[i].PublicKey = key.PublicKey()
	}
	setup.vs = NewValidatorSet(setup.validators, setup.f)
	return setup
}

func openCrashMain(t *testing.T, path string) kv.RwDB {
	t.Helper()
	db, err := mdbx.NewMDBX(log.New()).Path(path).MapSize(64 * datasize.MB).GrowthStep(4 * datasize.MB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.TableCfg{modules.HotStuffState: kv.TableCfgItem{}} }).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestIndependentVoteStoreCrashHelper(t *testing.T) {
	root := os.Getenv("N42_VOTE_CRASH_TEST_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	setup := deterministicVoteSetup(t)
	db := openCrashMain(t, filepath.Join(root, "main"))
	_, e, ch := prepareVoteStore(t, setup, db, filepath.Join(root, "votes"), true)
	e.roundState.AdvanceView(1)
	if err := importAndPropose(t, e, setup, 1, types.Hash{99}); err != nil {
		t.Fatal(err)
	}
	if findVote(drainOutputs(ch)) == nil {
		t.Fatal("vote was not released")
	}
	// Bypass Service.Stop, database Close and all test cleanup. Only a completed
	// durable journal transaction protects the emitted vote here.
	os.Exit(0)
}

func TestIndependentVoteStoreSurvivesProcessExit(t *testing.T) {
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestIndependentVoteStoreCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "N42_VOTE_CRASH_TEST_ROOT="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v\n%s", err, output)
	}
	db := openCrashMain(t, filepath.Join(root, "main"))
	t.Cleanup(db.Close)
	setup := deterministicVoteSetup(t)
	_, e, ch := prepareVoteStore(t, setup, db, filepath.Join(root, "votes"), false)
	state := e.SnapshotState()
	if state.LastVotedView != 1 || state.LastVotedHash != (types.Hash{99}) {
		t.Fatal("process exit lost vote")
	}
	_ = importAndPropose(t, e, setup, 1, types.Hash{100})
	if findVote(drainOutputs(ch)) != nil {
		t.Fatal("process restart released a conflicting vote")
	}
}
