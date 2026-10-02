/*
   Copyright 2022 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// lsTNewAggregator builds a fully wired *Aggregator over a temp dir and an
// in-memory MDBX database using the default chaindata table config (which
// already declares every table the aggregator's domains/histories/inverted
// indices need). Aggregation step is kept tiny so tests exercise step-file
// build/merge without large fixtures.
func lsTNewAggregator(t *testing.T, aggregationStep uint64) (string, kv.RwDB, *Aggregator) {
	t.Helper()
	logger := log.New()
	dir := t.TempDir()

	db := mdbx.NewMDBX(logger).InMem(dir).MustOpen()
	t.Cleanup(db.Close)

	agg, err := NewAggregator(context.Background(), dir, dir, aggregationStep, db, logger)
	require.NoError(t, err)
	t.Cleanup(agg.Close)

	return dir, db, agg
}
