// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Command mobileverify-packetd builds gov5 StreamPacket V2 packets for the
// blocks of a node that cannot build them itself (the n42-rs node) and hands
// them to that node's loopback packet ingest, so a fleet with no gov5 leader
// can serve phones.
//
// It follows the node's head over JSON-RPC, fetches each block as gov5 types
// (debug_getRawBlock when the debug namespace is served, else
// debug_getRawHeader / the block JSON plus
// eth_getRawTransactionByBlockHashAndIndex; the header must hash to the
// node's block hash), re-executes it with gov5's execution through the
// read-log recorder over a state reader answered by the node at the parent
// block (eth_getTransactionCount / eth_getBalance / eth_getCode /
// eth_getStorageAt), checks gas used and the receipts root against the
// header, and PUTs the packet to <mv>/mobileverify/packet.
//
// The head is polled with eth_blockNumber every -poll (default 250ms,
// minimum 50ms) while idle; a backlog is worked through without sleeping.
// The interval bounds how late a packet lands after its block: a phone
// reading /mobileverify/latest can see packet:false for up to that long.
//
// Usage:
//
//	mobileverify-packetd -rpc http://127.0.0.1:18545 -mv http://127.0.0.1:21010 \
//	  -genesis crates/chainspec/res/genesis/n42_devnet.json [-from N] [-out DIR] [-poll 250ms]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/streamverify/packetd"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/params"
)

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:18545", "JSON-RPC URL of the node whose blocks get packets")
	mv := flag.String("mv", "", "the node's mobileverify surface (PUT <mv>/mobileverify/packet); empty = no PUT")
	out := flag.String("out", "", "directory to write <hash>.bin packets into (optional)")
	genesis := flag.String("genesis", "", "genesis JSON whose chain config the blocks follow (overrides -chain)")
	chain := flag.String("chain", "", "gov5 params chain name (when no -genesis)")
	from := flag.Uint64("from", 0, "first block (0 = the node's current head)")
	poll := flag.Duration("poll", packetd.DefaultPoll, "head poll interval (eth_blockNumber) while idle, minimum 50ms; a backlog is processed without sleeping")
	conc := flag.Int("conc", 8, "concurrent JSON-RPC requests for the state prefetch")
	byNumber := flag.Bool("state-by-number", false, "read the parent state by block number instead of by hash (EIP-1898)")
	timeout := flag.Duration("rpc-timeout", 30*time.Second, "per-request JSON-RPC timeout")
	verbosity := flag.String("verbosity", "info", "log level: error, warn, info, debug")
	flag.Parse()
	if *poll < packetd.MinPoll {
		fatal("-poll %v is below the %v minimum", *poll, packetd.MinPoll)
	}

	lvl, err := log.LvlFromString(*verbosity)
	if err != nil {
		fatal("verbosity: %v", err)
	}
	log.Root().SetHandler(log.LvlFilterHandler(lvl, log.StderrHandler))

	var cfg *params.ChainConfig
	switch {
	case *genesis != "":
		if cfg, err = conf.LoadGenesisChainConfig(*genesis); err != nil {
			fatal("genesis %s: %v", *genesis, err)
		}
	case *chain != "":
		if cfg = params.ChainConfigByChainName(*chain); cfg == nil {
			fatal("unknown chain %q", *chain)
		}
	default:
		fatal("need -genesis <file> or -chain <name>")
	}
	if *mv == "" && *out == "" {
		fatal("nothing to do: give -mv and/or -out")
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fatal("out: %v", err)
		}
	}
	log.Info("packetd: chain config", "chainId", cfg.ChainID)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	f := &packetd.Follower{
		RPC:           packetd.NewClient(*rpcURL, *timeout),
		Chain:         cfg,
		IngestURL:     strings.TrimRight(*mv, "/"),
		OutDir:        *out,
		StateAtNumber: *byNumber,
		Concurrency:   *conc,
		Poll:          *poll,
	}
	if err := f.Run(ctx, *from); err != nil && ctx.Err() == nil {
		fatal("%v", err)
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "mobileverify-packetd: "+format+"\n", a...)
	os.Exit(1)
}
