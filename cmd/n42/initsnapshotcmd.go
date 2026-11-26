package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/cmd/utils"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/internal/snapshotjoin"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	"github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

var (
	snapshotBundleFlag = &cli.StringFlag{
		Name:  "bundle",
		Usage: "profile-1 snapshot bundle directory (written by n42-reth-state-dump --bundle)",
	}
	snapshotFromPeersFlag = &cli.StringSliceFlag{
		Name:  "from-peers",
		Usage: "fetch the snapshot from these peers over the v1.1 state-sync protocols (multiaddrs ending in /p2p/<id>) instead of --bundle",
	}
	snapshotPeerWaitFlag = &cli.DurationFlag{
		Name:  "from-peers.wait",
		Usage: "how long to wait for a peer to publish a state-sync pivot",
		Value: 5 * time.Minute,
	}

	initSnapshotCommand = &cli.Command{
		Name:      "init-snapshot",
		Usage:     "Initialize an empty datadir at a profile-1 snapshot bundle's pivot block",
		ArgsUsage: "<genesisPath>",
		Action:    initSnapshot,
		Flags: []cli.Flag{
			DataDirFlag,
			ChainFlag,
			ProfileFlag,
			snapshotBundleFlag,
			snapshotFromPeersFlag,
			snapshotPeerWaitFlag,
		},
		Description: `
The init-snapshot command writes the genesis like init, then installs the
chain's state at a committed pivot block P from an offline snapshot bundle
(gate 4a): reth init-state JSONL, header(P), the portable QMDB snapshot,
header(P+1), and a commit QC anchoring P+1. It verifies the QC against the
genesis validator set, the header chain, header(P+1).Root against the
rebuilt QMDB forest (deferred execution, k = 1) and the JSONL against the
forest's live leaves, and refuses on any mismatch. The node then starts at
head P and range-syncs the blocks above it. The datadir must be empty.

With --from-peers instead of --bundle, the same bundle is fetched from
running nodes over qmdb_status/1, commit_qc/1, bodies_by_range/2,
qmdb_manifest/1, qmdb_twigs/1 and code_by_hash/1 (N42 sync standard §5),
verified piece by piece as it arrives, and then installed as above.`,
	}
)

func initSnapshot(cliCtx *cli.Context) error {
	genesis, _ := prepareInitGenesis(cliCtx)
	if !genesis.Config.IsProfile1() {
		utils.Fatalf("init-snapshot: the genesis is not a profile-1 chain")
	}
	bundle := cliCtx.String(snapshotBundleFlag.Name)
	fromPeers := cliCtx.StringSlice(snapshotFromPeersFlag.Name)
	if (bundle == "") == (len(fromPeers) == 0) {
		utils.Fatalf("init-snapshot: give exactly one of --bundle and --from-peers")
	}

	chaindb, err := node.OpenDatabase(cliCtx.Context, &DefaultConfig, nil, kv.ChainDB.String())
	if err != nil {
		utils.Fatalf("Failed to open database: %v", err)
	}
	defer chaindb.Close()

	var (
		genesisBlock *block.Block
		res          *snapshotjoin.Result
	)
	if err := chaindb.Update(context.Background(), func(tx kv.RwTx) error {
		storedHash, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if storedHash != (types.Hash{}) {
			return fmt.Errorf("the datadir already holds a chain (genesis %x); init-snapshot needs an empty datadir", storedHash)
		}
		if genesisBlock, err = node.WriteGenesisBlock(tx, genesis); err != nil {
			return err
		}
		if err := node.WriteChainConfig(tx, genesisBlock.Hash(), genesis); err != nil {
			return err
		}
		if len(fromPeers) > 0 {
			// The genesis hash is known only now; the write transaction stays
			// open while the state downloads, so a refusal leaves nothing.
			if bundle, err = fetchSnapshotFromPeers(cliCtx, genesis.Config, genesisBlock.Hash(), fromPeers); err != nil {
				return err
			}
			defer os.RemoveAll(bundle)
		}
		res, err = snapshotjoin.Import(tx, genesis.Config, genesisBlock.Hash(), bundle, nil)
		return err
	}); err != nil {
		utils.Fatalf("init-snapshot refused, nothing written: %v", err)
	}
	if err := node.PersistDataDirNetworkBinding(&DefaultConfig, genesis.Config, genesisBlock.Hash()); err != nil {
		utils.Fatalf("Failed to persist datadir network binding: %v", err)
	}
	log.Info("Snapshot installed", "genesis", genesisBlock.Hash(), "pivot", res.Pivot, "hash", res.PivotHash,
		"postStateRoot", res.Root, "accounts", res.Accounts, "storageSlots", res.Slots, "anchorView", res.AnchorView,
		"anchorBlock", res.AnchorNumber)
	fmt.Printf("init-snapshot: genesis %s, head %d %s, post-state root %s (header(%d).Root), %d accounts, %d slots, %d live leaves, anchor QC view %d on block %d\n",
		genesisBlock.Hash().Hex(), res.Pivot, res.PivotHash.Hex(), res.Root.Hex(), res.Pivot+1, res.Accounts, res.Slots, res.LiveLeaves, res.AnchorView, res.AnchorNumber)
	return nil
}

// fetchSnapshotFromPeers downloads a verified bundle from peers into a fresh
// directory under the datadir and returns it.
func fetchSnapshotFromPeers(cliCtx *cli.Context, cfg *params.ChainConfig, genesisHash types.Hash, addrs []string) (string, error) {
	ctx := cliCtx.Context
	h, peers, err := n42sync.DialStateSyncPeers(ctx, genesisHash, addrs)
	if err != nil {
		return "", err
	}
	defer h.Close()
	dir, err := os.MkdirTemp(DefaultConfig.NodeCfg.DataDir, "state-sync-bundle-")
	if err != nil {
		return "", err
	}
	if _, err := v11.FetchBundle(ctx, peers, v11.JoinOptions{
		Config: cfg, Genesis: genesisHash, Dir: dir, StatusWait: cliCtx.Duration(snapshotPeerWaitFlag.Name), Logger: log.New(),
	}); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
