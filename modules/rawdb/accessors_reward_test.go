// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package rawdb

import (
	"context"
	"runtime"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
	"golang.org/x/sync/semaphore"
)

func OpenDatabase() (kv.RwDB, error) {
	var chainKv kv.RwDB
	var err error
	logger := log2.New()

	dbPath := "./mdbx.db"

	var openFunc = func(exclusive bool) (kv.RwDB, error) {
		roTxsLimiter := semaphore.NewWeighted(int64(cmp.Max(32, runtime.GOMAXPROCS(-1)*8))) // 1 less than max to allow unlocking to happen
		opts := mdbx.NewMDBX(logger).
			WriteMergeThreshold(4 * 8192).
			Path(dbPath).Label(kv.ChainDB).
			DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter)
		if exclusive {
			opts = opts.Exclusive()
		}

		modules.N42Init()
		kv.ChaindataTablesCfg = modules.N42TableCfg

		opts = opts.MapSize(8 * datasize.TB)
		return opts.Open(context.Background())
	}
	chainKv, err = openFunc(false)
	if err != nil {
		return nil, err
	}

	if err = chainKv.Update(context.Background(), func(tx kv.RwTx) (err error) {
		return params.SetN42Version(tx, params.VersionKeyCreated)
	}); err != nil {
		return nil, err
	}
	return chainKv, nil
}
