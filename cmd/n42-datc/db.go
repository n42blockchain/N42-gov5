// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// modulesInit registers the chaindata table schema (needed before any MDBX
// open, build or read-only).
func modulesInit() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

// datcTables are the prototype-local tables registered on top of the
// chaindata schema (the build also needs Hashed*/TrieOf* from it).
var datcTables = []string{tDatcAccNode, tDatcStoNode, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS, tDatcStoRoot, tDatcMeta, tFwdAcctCS, tFwdStorCS, tDatcRoots}

// openDatcDB opens (creating if needed) the DATC output MDBX with the DATC
// tables registered. DirtySpace: the kv default is 128 MB — a heavy window
// dirties 2-3 GB of Hashed*/node pages, so every window was SPILLING dirty
// pages to disk ~20x over; a multi-GB dirty space keeps a full batch's dirty
// set in RAM and commit then writes it once.
func openDatcDB(logger log.Logger, path string, mapGB, dirtyGB int) (kv.RwDB, error) {
	return mdbxkv.NewMDBX(logger).Path(path).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(mapGB) * datasize.GB).
		DirtySpace(uint64(dirtyGB) * uint64(datasize.GB)).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for name, item := range kv.ChaindataTablesCfg {
				d[name] = item
			}
			for _, t := range datcTables {
				d[t] = kv.TableCfgItem{}
			}
			return d
		}).Open(context.Background())
}
