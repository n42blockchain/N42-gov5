// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// eth-el is N42's Ethereum mainnet execution-layer node. It bootstraps
// PlainState from a BitTorrent-distributed leaves journal, catches up
// from the resulting recent height to chain tip via downloaded segments,
// and then exposes the standard Engine API for a CL (external Lighthouse
// or embedded Caplin) to drive live block validation.
//
// See internal/ethel/ARCHITECTURE.md for the full design.

package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	storagetorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
	"github.com/n42blockchain/N42/internal/ethel/bootstrap"
	"github.com/n42blockchain/N42/internal/ethel/catchup"
	"github.com/n42blockchain/N42/internal/ethel/coldresolve"
	"github.com/n42blockchain/N42/internal/ethel/coldseed"
	"github.com/n42blockchain/N42/internal/ethel/eldevp2p"
	"github.com/n42blockchain/N42/internal/ethel/engineapi"
	"github.com/n42blockchain/N42/internal/ethel/fetch"
	"github.com/n42blockchain/N42/internal/ethel/snapshotprestart"
	"github.com/n42blockchain/N42/internal/ethel/snapshotreader"
	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

func main() {
	app := &cli.App{
		Name:     "eth-el",
		Usage:    "N42 Ethereum mainnet execution-layer node",
		Flags:    flags(),
		Action:   run,
		Commands: []*cli.Command{snapshot.DownloadCommand()},
	}

	// pprof endpoint for ad-hoc profiling.
	go func() { _ = http.ListenAndServe("localhost:6061", nil) }()

	if err := app.Run(os.Args); err != nil {
		log.Error("eth-el exited", "err", err)
		os.Exit(1)
	}
}

func flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "datadir", Usage: "Working directory (chaindata, freezer, torrent, caplin)", Required: true},
		&cli.StringFlag{Name: "network", Usage: "Network preset (mainnet|sepolia|holesky|hoodi)", Value: "mainnet"},

		// Storage tuning.
		&cli.Uint64Flag{Name: "storage.mapsize.gb", Usage: "MDBX mmap reservation, GiB", Value: 64},
		&cli.Uint64Flag{Name: "storage.pagesize", Usage: "MDBX page size, bytes", Value: 4096},

		// Bootstrap.
		&cli.BoolFlag{Name: "bootstrap.enabled", Usage: "Run leaves-journal bootstrap on startup", Value: true},
		&cli.StringFlag{Name: "bootstrap.mode", Usage: "Bootstrap mechanism: leaves (download journal + RebuildState, archive) | snapshot (snapshot-direct via warm overlay, minimal/full) | none. Empty = compat with --bootstrap.enabled"},
		&cli.StringFlag{Name: "bootstrap.manifest", Usage: "Manifest URL or magnet for the leaves journal segment set"},
		&cli.Uint64Flag{Name: "bootstrap.end-block", Usage: "Cap the rebuild range (0 = use all leaves)"},

		// Catch-up.
		&cli.StringFlag{Name: "catchup.manifest", Usage: "Segments manifest URL or file path (empty: local freezer only)"},
		&cli.Uint64Flag{Name: "catchup.commit-interval", Usage: "MDBX commit interval (blocks)", Value: 10000},
		&cli.Uint64Flag{Name: "catchup.verify-interval", Usage: "State-root verification interval (0 = disabled)"},

		// Engine API.
		&cli.BoolFlag{Name: "engine.enabled", Usage: "Serve Engine API for the CL", Value: true},
		&cli.StringFlag{Name: "engine.host", Usage: "Engine API listen host", Value: "127.0.0.1"},
		&cli.IntFlag{Name: "engine.port", Usage: "Engine API listen port", Value: 20014},
		&cli.StringFlag{Name: "engine.jwt", Usage: "Path to the 32-byte JWT shared secret file (required when engine.enabled)"},

		// WebRTC fetcher (direct DataChannel; not WebTorrent).
		&cli.BoolFlag{Name: "webrtc.enabled", Usage: "Enable the WebRTC DataChannel fetcher (webrtc: sources in manifests)"},

		// BitTorrent downloader (shared with bootstrap + catchup fetchers).
		&cli.BoolFlag{Name: "torrent.enabled", Usage: "Enable the BitTorrent fetcher (magnet links in manifests)"},
		&cli.StringFlag{Name: "torrent.datadir", Usage: "BitTorrent working directory (default: <datadir>/torrent)"},
		&cli.StringFlag{Name: "torrent.listen", Usage: "BitTorrent listen address", Value: ":42069"},
		&cli.IntFlag{Name: "torrent.upload.kbps", Usage: "Upload rate limit in KB/s (0 = unlimited)"},
		&cli.IntFlag{Name: "torrent.download.kbps", Usage: "Download rate limit in KB/s (0 = unlimited)"},
		&cli.BoolFlag{Name: "torrent.dht", Usage: "Enable DHT peer discovery", Value: true},
		&cli.BoolFlag{Name: "torrent.pex", Usage: "Enable PEX peer exchange", Value: true},

		// EIP-4444 history expiry: Full node keeps a recent window of bodies hot
		// and fetches cold (offloaded) segments on demand; archive node seeds all.
		&cli.StringFlag{Name: "history.mode", Usage: "History profile: full (recent window hot, cold fetched on demand) | archive (download + serve full history). Empty = neither"},
		&cli.StringFlag{Name: "history.coldmanifest", Usage: "Cold/seed manifest (JSON). Full: on-demand cold fetch; archive: eager full-history download"},
		&cli.StringFlag{Name: "history.coldcache", Usage: "Local cache dir for fetched cold segments (default: <torrent.datadir>/cold)"},
		&cli.StringFlag{Name: "history.colddir", Usage: "Local cold dir holding offloaded cdat (use instead of torrent fetch)"},
		&cli.BoolFlag{Name: "history.seed", Usage: "Archive node: seed ALL columnar files (headers+bodies+witness) over torrent for 1-of-N"},
		&cli.StringFlag{Name: "history.seed.dir", Usage: "Freezer dir to seed (archive node; default: the output freezer dir)"},
		&cli.StringFlag{Name: "history.seed.prefixes", Usage: "Comma list of file prefixes to seed", Value: "bodyc,headerc,receipts"},
		&cli.StringFlag{Name: "history.seed.manifest", Usage: "Seed manifest output path (archive node)", Value: "seed-manifest.json"},
		&cli.DurationFlag{Name: "history.seed.interval", Usage: "Re-seed interval; the active (growing) file is re-seeded each pass", Value: 168 * time.Hour},
		&cli.StringFlag{Name: "history.f2dir", Usage: "F2 (no-signature) body store dir. When set, the node can serve ledger bodies (from/to/value/nonce/gas/input) from it — ~45% smaller, no sig/hash"},

		// Caplin (embedded CL). Inert unless built with -tags n42el.
		&cli.BoolFlag{Name: "caplin.enabled", Usage: "Run an embedded Caplin consensus layer (requires -tags n42el)"},
		&cli.StringFlag{Name: "caplin.network", Usage: "Caplin network preset", Value: "mainnet"},
		&cli.StringFlag{Name: "caplin.datadir", Usage: "Caplin data directory (default: <datadir>/caplin)"},
		&cli.IntFlag{Name: "caplin.sentinel.port", Usage: "Caplin sentinel libp2p TCP port", Value: 9000},
		&cli.IntFlag{Name: "caplin.sentinel.discovery.port", Usage: "Caplin sentinel discv5 UDP port", Value: 9000},
		&cli.StringFlag{Name: "caplin.checkpoint.url", Usage: "Beacon checkpoint-sync URL"},
		&cli.StringFlag{Name: "caplin.discovery.addr", Usage: "Caplin discv5/libp2p bind IP", Value: "0.0.0.0"},
		&cli.StringFlag{Name: "caplin.nat", Usage: "Caplin external IP for ENR/multiaddr (extip:<ip> | none)"},
		&cli.StringSliceFlag{Name: "caplin.bootnodes", Usage: "Extra Caplin ENR bootnodes (appended to the network preset)"},
		&cli.Uint64Flag{Name: "caplin.max-peer-count", Usage: "Max Caplin beacon peers", Value: 64},

		// Snapshot mirror pre-start sync (Stage 2 G3).
		&cli.StringFlag{Name: "snapshot.source", Usage: "Publisher mirror URL (file:// or http(s)://). When set, runs status + auto-catchup before Engine API opens"},
		&cli.StringFlag{Name: "snapshot.mode", Usage: "Snapshot mode (minimal|full|archive)", Value: "archive"},
		&cli.Uint64Flag{Name: "snapshot.max-gap", Usage: "Refuse auto-catchup if behind by more than N blocks (0 = no cap)", Value: 1_000_000},
		&cli.IntFlag{Name: "snapshot.max-iterations", Usage: "Per-CatchUp delta-apply iteration cap (0 = no cap)"},
		&cli.DurationFlag{Name: "snapshot.timeout", Usage: "Total budget for the pre-start sync"},
		&cli.BoolFlag{Name: "snapshot.auto-fetch", Usage: "When datadir is empty (first boot), auto-fetch the initial archive from --snapshot.source before catch-up. Default false because initial fetch can be many GB."},
		&cli.IntFlag{Name: "snapshot.fetch-parallel", Usage: "Worker count for the initial AutoFetch download (0 = default 4)", Value: 8},

		// EL devp2p — direct mainnet EL p2p sync, bypasses CL when beacon p2p (port 9000) is blocked.
		&cli.BoolFlag{Name: "eldevp2p.enabled", Usage: "Run an embedded Ethereum devp2p (eth/68-69) listener so eth-el catches up via EL p2p directly, bypassing a CL"},
		&cli.StringFlag{Name: "eldevp2p.listen", Usage: "EL devp2p TCP listen address (default :30303)", Value: ":30303"},
		&cli.IntFlag{Name: "eldevp2p.max-peers", Usage: "Maximum simultaneous EL devp2p peers", Value: 50},
		&cli.StringSliceFlag{Name: "eldevp2p.bootnodes", Usage: "Extra enode:// URLs appended to the default mainnet bootnodes (repeatable, or comma-separated)"},
		&cli.BoolFlag{Name: "eldevp2p.bootnodes-replace", Usage: "When set, use ONLY --eldevp2p.bootnodes entries (don't merge with the built-in mainnet defaults)"},
		&cli.BoolFlag{Name: "hashed-canonical", Usage: "reth-2.2-style hashed-canonical state: EVM reads from HashedAccounts/HashedStorage (no PlainState), incremental root over migrated TrieOf*. Use for datadirs built by n42-migrate-reth-hashed."},

		// Stage 2 G4 — auto mode selection.
		&cli.StringFlag{Name: "catch-up.mode", Usage: "Catch-up mechanism: auto|off|delta|libp2p|fetch (auto picks based on gap)", Value: "auto"},
		&cli.Uint64Flag{Name: "catch-up.delta-window", Usage: "Publisher's typical delta retention (blocks). Auto switches to libp2p when gap > this. 0 = unknown", Value: 1_000_000},
	}
}

func run(c *cli.Context) error {
	cfg := assembleConfig(c)

	// Stage 2 G3: optional pre-start sync against a publisher mirror.
	// If --snapshot.source is set, we run snapshot status + auto-catchup
	// BEFORE building the node so the Engine API opens against a
	// current datadir. The gap is capped via --snapshot.max-gap to
	// avoid multi-hour catchup loops on first boot — operators
	// fetch the initial archive with `n42-eth-snapshot fetch` and
	// only rely on auto-catchup for the per-cycle delta.
	if src := c.String("snapshot.source"); src != "" {
		rep, err := snapshotprestart.PreStartSync(c.Context, snapshotprestart.Config{
			Datadir:       cfg.DataDir,
			Source:        src,
			Mode:          c.String("snapshot.mode"),
			MaxBlocks:     c.Uint64("snapshot.max-gap"),
			MaxIter:       c.Int("snapshot.max-iterations"),
			Timeout:       c.Duration("snapshot.timeout"),
			ModeRequest:   c.String("catch-up.mode"),
			DeltaWindow:   c.Uint64("catch-up.delta-window"),
			AutoFetch:     c.Bool("snapshot.auto-fetch"),
			FetchParallel: c.Int("snapshot.fetch-parallel"),
			// libp2p availability is true if eth-el has the
			// catchup pipeline wired (which it always does via
			// catchup.New below). When upstream peers aren't
			// reachable, the libp2p stage will surface that
			// at sync time — not at pre-start.
			Libp2pAvailable: true,
		})
		if err != nil {
			log.Error("eth-el: snapshot pre-start sync failed",
				"err", err, "skipped", rep != nil && rep.Skipped,
				"gap", func() uint64 {
					if rep != nil {
						return rep.GapAtStart
					}
					return 0
				}())
			return fmt.Errorf("snapshot pre-start: %w", err)
		}
		if rep.Skipped {
			log.Info("eth-el: snapshot pre-start skipped (no source)")
		} else if rep.WasCurrent {
			log.Info("eth-el: snapshot pre-start OK (already current)",
				"height", rep.FinalHeight, "strategy", rep.Strategy)
		} else {
			log.Info("eth-el: snapshot pre-start ran",
				"strategy", rep.Strategy,
				"deltas", rep.DeltasApplied,
				"initial_fetched", rep.InitialFetched,
				"initial_files", rep.InitialFetchFiles,
				"initial_bytes_mb", rep.InitialFetchBytes/(1<<20),
				"start_height", rep.StartHeight,
				"final_height", rep.FinalHeight,
				"elapsed_ms", rep.ElapsedMS)
			if rep.WarnMessage != "" {
				log.Warn("eth-el: snapshot pre-start partial", "msg", rep.WarnMessage)
			}
		}
	}

	node, err := ethel.NewNode(cfg)
	if err != nil {
		return fmt.Errorf("new node: %w", err)
	}

	// Shared download layer. HTTPFetcher is always wired; TorrentFetcher
	// is added only when --torrent.enabled is set so the default
	// configuration does not spin up a DHT + libp2p listener just to
	// pull a single HTTPS manifest.
	//
	// Both bootstrap and catchup factory services consume this same
	// Fetcher instance so the torrent client is shared (and so is its
	// upload bandwidth accounting).
	fetchers := []fetch.Fetcher{
		fetch.NewHTTPFetcher(fetch.HTTPFetcherOptions{}),
	}
	if c.Bool("webrtc.enabled") {
		fetchers = append(fetchers, fetch.NewWebRTCFetcher(fetch.WebRTCFetcherOptions{}))
		log.Info("eth-el: webrtc fetcher enabled")
	}
	var torrentClient *storagetorrent.Client
	if cfg.Torrent.Enabled {
		torrentClient, err = storagetorrent.NewClient(&cfg.Torrent)
		if err != nil {
			return fmt.Errorf("start torrent client: %w", err)
		}
		torrentClient.Start()
		defer torrentClient.Stop()
		fetchers = append(fetchers, fetch.NewTorrentFetcher(torrentClient, fetch.TorrentFetcherOptions{}))
		log.Info("eth-el: torrent fetcher enabled",
			"listen", cfg.Torrent.ListenAddr,
			"dht", cfg.Torrent.EnableDHT,
			"datadir", cfg.Torrent.DataDir,
		)
	}
	fetcher := fetch.NewMultiSourceFetcher(fetchers...)

	// F2 ledger store (no-signature bodies, ~45% smaller). When configured, the
	// node opens it and can serve ledger body fields (from/to/value/nonce/gas/
	// input) from it; signatures and canonical tx hashes are not reproducible
	// (served via the MPHF hash index / F1.5, separately).
	if f2dir := c.String("history.f2dir"); f2dir != "" {
		f2r, err := bodyf2.OpenReader(f2dir)
		if err != nil {
			return fmt.Errorf("open F2 store %q: %w", f2dir, err)
		}
		ethel.SetF2Reader(f2r)
		log.Info("eth-el: F2 ledger store opened", "dir", f2dir, "segments", f2r.Segments())
		// Optional tx-hash -> (block,index) MPHF index → getTransactionByHash (F1.5).
		if hr, herr := history.OpenMPHF(f2dir, "f2.txhash"); herr == nil {
			ethel.SetF2HashIndex(hr)
			log.Info("eth-el: F2 tx-hash index opened", "keys", hr.KeyCount())
		}
		// Optional per-block tx-hash sidecar → fullTx=false hash lists.
		if hsr, herr := bodyf2.OpenHashReader(f2dir); herr == nil {
			ethel.SetF2Hashes(hsr)
			log.Info("eth-el: F2 tx-hash sidecar opened (fullTx=false hash lists)")
		}
	}

	// EIP-4444 transparent cold reads (Full node). With a cold manifest, trimmed
	// cold body segments are fetched on demand — over torrent (1-of-N, any
	// archive/seeder serving the infohash) when the torrent client is up, else
	// from a local cold dir. Installed before services open their readers.
	if cm := c.String("history.coldmanifest"); cm != "" {
		m, err := torrentsync.LoadManifest(cm)
		if err != nil {
			return fmt.Errorf("load cold manifest %q: %w", cm, err)
		}
		cacheDir := c.String("history.coldcache")
		if cacheDir == "" {
			cacheDir = filepath.Join(cfg.Torrent.DataDir, "cold")
		}
		var cf coldresolve.Fetcher
		switch {
		case c.String("history.colddir") != "":
			cf = coldresolve.LocalDirFetcher{ColdDir: c.String("history.colddir")}
		case torrentClient != nil:
			cf = coldresolve.NewTorrentFetcher(storagetorrent.NewBridge(torrentClient), cacheDir, 10*time.Minute)
		default:
			return fmt.Errorf("history.coldmanifest set but neither --history.colddir nor --torrent.enabled provided")
		}
		ethel.SetDefaultColdResolver(coldresolve.New(m, cf, true))
		log.Info("eth-el: EIP-4444 cold-read resolver installed",
			"manifest", cm, "segments", len(m.Segments), "cache", cacheDir,
			"fetch", map[bool]string{true: "local-dir", false: "torrent(1-of-N)"}[c.String("history.colddir") != ""])

		// Receipts use a per-block freezer index (not the 8B/seg columnar), so
		// they get a name-keyed resolver installed on the freezer's receipts
		// table once it is open (freezer maps blockNum→fileNum; resolver fetches
		// the trimmed file by name). Same manifest + fetcher.
		fr := coldresolve.NewFreezerResolver(m, cf, true)
		node.RegisterFactory(func(n *ethel.Node) ethel.Service {
			return coldresolve.NewFreezerInstallService(n.OutFreezer(), "receipts", fr)
		})
	}

	// Archive history profile: explicitly download the FULL history (every file
	// in the manifest: headers+bodies+witness+receipts) from the swarm at boot,
	// rather than relying only on on-demand cold reads. Distinct from the Full
	// profile, which keeps a recent window hot and fetches cold lazily.
	if c.String("history.mode") == "archive" {
		cm := c.String("history.coldmanifest")
		if cm == "" {
			return fmt.Errorf("--history.mode archive requires --history.coldmanifest (the full-history manifest)")
		}
		if torrentClient == nil {
			return fmt.Errorf("--history.mode archive requires --torrent.enabled")
		}
		m, err := torrentsync.LoadManifest(cm)
		if err != nil {
			return fmt.Errorf("load history manifest %q: %w", cm, err)
		}
		store := c.String("history.coldcache")
		if store == "" {
			store = cfg.Torrent.DataDir // operator should point at the freezer dir
		}
		tf := coldresolve.NewTorrentFetcher(storagetorrent.NewBridge(torrentClient), store, 30*time.Minute)
		node.RegisterFactory(func(n *ethel.Node) ethel.Service {
			return coldresolve.NewDownloadService(m, tf, true)
		})
		log.Info("eth-el: archive full-history download registered", "manifest", cm, "segments", len(m.Segments), "store", store)
	}

	// Archive node: seed ALL columnar files (headers+bodies+witness) so other
	// nodes can pull full history under 1-of-N. The active (growing) file is
	// re-seeded each interval. Requires the torrent client.
	if c.Bool("history.seed") {
		if torrentClient == nil {
			return fmt.Errorf("--history.seed requires --torrent.enabled")
		}
		seedDir := c.String("history.seed.dir")
		if seedDir == "" {
			seedDir = cfg.Torrent.DataDir // operator should point this at the freezer dir
		}
		bridge := storagetorrent.NewBridge(torrentClient)
		opts := coldseed.Options{
			Dir:          seedDir,
			Prefixes:     strings.Split(c.String("history.seed.prefixes"), ","),
			ManifestPath: c.String("history.seed.manifest"),
			ChainID:      1, // eth-el is Ethereum mainnet EL
			PieceSize:    cfg.Torrent.PieceSize,
			Interval:     c.Duration("history.seed.interval"),
		}
		node.RegisterFactory(func(n *ethel.Node) ethel.Service {
			return coldseed.NewService(opts, coldseed.BridgeSink{Bridge: bridge})
		})
		log.Info("eth-el: archive history seeder registered",
			"dir", seedDir, "prefixes", opts.Prefixes, "interval", opts.Interval)
	}

	// Factory-registered services run in registration order. Order
	// matters: bootstrap lands a PlainState before catch-up tries to
	// replay on top of it; engineAPI comes after both so it is
	// already listening when the CL reconnects after a restart.
	node.RegisterFactory(func(n *ethel.Node) ethel.Service {
		return bootstrap.New(cfg.Bootstrap, n, fetcher)
	})
	node.RegisterFactory(func(n *ethel.Node) ethel.Service {
		return catchup.New(catchup.Config{
			CatchUp:  cfg.CatchUp,
			Manifest: cfg.CatchUp.Manifest,
		}, n, fetcher)
	})
	node.RegisterFactory(func(n *ethel.Node) ethel.Service {
		return engineapi.New(cfg.EngineAPI, n.ChainConfig(), n.Engine(), n.RwDB(), n.OutFreezer())
	})

	// EL devp2p service — bypasses the CL entirely. Dials Ethereum mainnet
	// peers on TCP 30303 directly so eth-el can catch up its head from EL
	// p2p when CL beacon p2p (port 9000) is filtered by the operator's ISP.
	// Off by default; enable via --eldevp2p.enabled.
	node.RegisterFactory(func(n *ethel.Node) ethel.Service {
		eldcfg := eldevp2p.DefaultConfig()
		eldcfg.Enabled = c.Bool("eldevp2p.enabled")
		if addr := c.String("eldevp2p.listen"); addr != "" {
			eldcfg.ListenAddr = addr
		}
		if mp := c.Int("eldevp2p.max-peers"); mp > 0 {
			eldcfg.MaxPeers = mp
		}
		if extra := c.StringSlice("eldevp2p.bootnodes"); len(extra) > 0 {
			if c.Bool("eldevp2p.bootnodes-replace") {
				eldcfg.BootNodes = append([]string{}, extra...)
			} else {
				eldcfg.BootNodes = append(eldcfg.BootNodes, extra...)
			}
		}
		eldcfg.HashedCanonical = c.Bool("hashed-canonical")
		// snapshot-direct (minimal/full): overlay the warm MDBX on the
		// immutable H0 snapshot segment. Open it here so the downloader's
		// adapter reads via WarmOverlayReader + OverlayStateWriter.
		if cfg.Bootstrap.Mode == "snapshot" {
			if cold, err := openSnapshotCold(cfg.DataDir); err != nil {
				log.Error("eth-el: snapshot-direct open failed, falling back to plain state", "err", err)
			} else {
				eldcfg.SnapshotCold = cold
				log.Info("eth-el: snapshot-direct enabled (warm overlay on H0 snapshot)")
			}
		}
		// Mainnet genesis hash + time. These are well-known constants;
		// pin them here rather than read from rawdb so the devp2p handshake
		// produces deterministic ForkID computation even before any block
		// has been written.
		genesisHash := types.HexToHash("0xd4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3")
		genesisTime := uint64(1438269988) // 2015-07-30 15:26:28 UTC
		return eldevp2p.New(eldcfg, n, genesisHash, genesisTime)
	})

	ctx, cancel := withShutdown()
	defer cancel()

	// Optional Caplin sidecar. The wire layer is split into beacon_wire.go
	// (n42el) and beacon_wire_stub.go (!n42el). Default builds resolve to
	// the stub so this call is free of side effects.
	caplin, err := startCaplin(ctx, cfg.Beacon, node)
	if err != nil {
		return fmt.Errorf("start caplin: %w", err)
	}
	defer caplin.Stop()

	if err := node.Start(ctx); err != nil {
		return fmt.Errorf("start node: %w", err)
	}
	defer node.Stop()

	<-ctx.Done()
	log.Info("eth-el: shutdown signal received")
	return nil
}

func assembleConfig(c *cli.Context) conf.EthELCfg {
	cfg := conf.DefaultEthELCfg()

	cfg.DataDir = c.String("datadir")
	cfg.Network = c.String("network")

	cfg.Storage.MapSize = datasize.ByteSize(c.Uint64("storage.mapsize.gb")) * datasize.GB
	if v := c.Uint64("storage.pagesize"); v != 0 {
		cfg.Storage.PageSize = v
	}

	cfg.Bootstrap.Enabled = c.Bool("bootstrap.enabled")
	cfg.Bootstrap.Mode = c.String("bootstrap.mode")
	cfg.Bootstrap.Manifest = c.String("bootstrap.manifest")
	cfg.Bootstrap.EndBlock = c.Uint64("bootstrap.end-block")

	cfg.CatchUp.Manifest = c.String("catchup.manifest")
	cfg.CatchUp.CommitInterval = c.Uint64("catchup.commit-interval")
	cfg.CatchUp.VerifyInterval = c.Uint64("catchup.verify-interval")

	cfg.EngineAPI.Enabled = c.Bool("engine.enabled")
	cfg.EngineAPI.Host = c.String("engine.host")
	cfg.EngineAPI.Port = c.Int("engine.port")
	cfg.EngineAPI.JWTSecretPath = c.String("engine.jwt")

	cfg.Torrent.Enabled = c.Bool("torrent.enabled")
	if tdir := c.String("torrent.datadir"); tdir != "" {
		cfg.Torrent.DataDir = tdir
	} else {
		cfg.Torrent.DataDir = filepath.Join(cfg.DataDir, "torrent")
	}
	cfg.Torrent.ListenAddr = c.String("torrent.listen")
	cfg.Torrent.UploadRateKB = c.Int("torrent.upload.kbps")
	cfg.Torrent.DownloadRateKB = c.Int("torrent.download.kbps")
	cfg.Torrent.EnableDHT = c.Bool("torrent.dht")
	cfg.Torrent.EnablePEX = c.Bool("torrent.pex")

	cfg.Beacon.Enabled = c.Bool("caplin.enabled")
	cfg.Beacon.Network = c.String("caplin.network")
	cfg.Beacon.DataDir = c.String("caplin.datadir")
	cfg.Beacon.SentinelPort = c.Int("caplin.sentinel.port")
	cfg.Beacon.SentinelDiscoveryPort = c.Int("caplin.sentinel.discovery.port")
	cfg.Beacon.CheckpointSyncURL = c.String("caplin.checkpoint.url")
	cfg.Beacon.DiscoveryAddr = c.String("caplin.discovery.addr")
	cfg.Beacon.NAT = c.String("caplin.nat")
	cfg.Beacon.BootnodesENR = c.StringSlice("caplin.bootnodes")
	cfg.Beacon.MaxPeerCount = c.Uint64("caplin.max-peer-count")

	return cfg
}

// withShutdown returns a context that cancels on the first SIGINT/SIGTERM
// and forces an exit on the second. The pattern matches cmd/ethexec so
// behavior is consistent across N42 binaries.
func withShutdown() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info("eth-el: shutdown signal received, draining...")
		cancel()
		<-sig
		log.Warn("eth-el: second signal — forcing exit")
		os.Exit(1)
	}()
	return ctx, cancel
}

// openSnapshotCold opens the H0 snapshot segment under <dataDir>/snapshot and
// wraps it as a cold StateReader for snapshot-direct (minimal/full) mode.
func openSnapshotCold(dataDir string) (state.StateReader, error) {
	snapDir := filepath.Join(dataDir, "snapshot")
	accPrefix, err := snapshotPrefix(snapDir, "accounts")
	if err != nil {
		return nil, err
	}
	stoPrefix, err := snapshotPrefix(snapDir, "storage")
	if err != nil {
		return nil, err
	}
	seg, err := snapshotreader.OpenSegment(snapDir, accPrefix, stoPrefix)
	if err != nil {
		return nil, err
	}
	// H0 contract bytecode lives in the codes freezer (codes.cidx + .cdat,
	// shipped in every snapshot mode). Wire it as the StateReader's CodeSource
	// so CALL targets resolve. Optional: if absent, H0 contract code reads
	// empty (warm/catch-up-deployed code still resolves via the warm Code table).
	var codeSrc state.CodeSource
	if cr, cerr := ethel.NewCodesFreezerReader(filepath.Join(dataDir, "chain", "freezer")); cerr == nil {
		codeSrc = cr
	} else {
		log.Warn("eth-el: snapshot-direct: codes freezer unavailable, H0 contract code reads empty", "err", cerr)
	}
	return snapshotreader.NewStateReader(seg, codeSrc), nil
}

// snapshotPrefix finds a table's segment prefix (e.g. "accounts.0-25999999") by
// globbing <dir>/<table>*.idx and stripping ".idx".
func snapshotPrefix(dir, table string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, table+"*.idx"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no %s snapshot segment (.idx) under %s", table, dir)
	}
	return strings.TrimSuffix(filepath.Base(matches[0]), ".idx"), nil
}
