package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestPerformanceMetadataInitUsesSameGenesisWithoutLocalState(t *testing.T) {
	t.Setenv("N42_MDBX_MAPSIZE_GB", "1")
	saved := DefaultConfig
	t.Cleanup(func() { DefaultConfig = saved })
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var g conf.Genesis
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		key, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		g.Config.HotStuff.Validators = append(g.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: "0x" + strings.Repeat("0", 39) + string("1234567"[i]), BLSKey: "0x" + hex.EncodeToString(key.PublicKey().Marshal())})
	}
	doc := map[string]any{"config": g.Config, "timestamp": g.Timestamp, "extraData": "0x" + hex.EncodeToString(g.ExtraData), "gasLimit": g.GasLimit, "difficulty": "0x0", "alloc": map[string]any{"0x0000000000000000000000000000000000001234": map[string]string{"balance": "0x100"}}}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	DefaultConfig = conf.Config{NodeCfg: conf.NodeConfig{DataDir: filepath.Join(t.TempDir(), "metadata")}}
	set := flag.NewFlagSet("metadata-init", flag.ContinueOnError)
	for _, f := range []cli.Flag{ChainFlag, ProfileFlag, performanceMetadataInitFlag} {
		if err := f.Apply(set); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Parse([]string{"--chain", "private", "--profile", "n42", "--performance.metadata-only", path}); err != nil {
		t.Fatal(err)
	}
	ctx := cli.NewContext(cli.NewApp(), set, nil)
	ctx.Context = context.Background()
	if err := initGenesis(ctx); err != nil {
		t.Fatal(err)
	}
	expected, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		t.Fatal(err)
	}
	db, err := node.OpenDatabase(ctx.Context, &DefaultConfig, nil, kv.ChainDB.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.View(ctx.Context, func(tx kv.Tx) error {
		bound, err := sharedvalidator.IsMetadataStore(tx)
		if err != nil {
			return err
		}
		stored, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if !bound || stored != expected.Hash() {
			t.Fatal("metadata genesis differs from owner genesis")
		}
		cursor, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer cursor.Close()
		key, _, err := cursor.First()
		if key != nil {
			t.Fatal("metadata init wrote funded alloc accounts")
		}
		if err != nil {
			return err
		}
		_, _, applied, err := rawdb.ReadQMDBApplied(tx)
		if applied {
			t.Fatal("metadata init fabricated applied state")
		}
		return err
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	DefaultConfig.NodeCfg.PerformanceGenesis = path
	stack, err := node.NewNode(ctx, &DefaultConfig)
	if stack != nil || err == nil || !strings.Contains(err.Error(), "metadata store cannot start a full execution node") {
		if stack != nil {
			stack.Close()
		}
		t.Fatalf("metadata store entered full execution constructor: %v", err)
	}
}
