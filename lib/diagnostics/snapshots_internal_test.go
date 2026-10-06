package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
)

func TestSnapshotUpdatersAndReadersRoundTrip(t *testing.T) {
	c := newTestClient(t)

	wantDownload := SnapshotDownloadStatistics{}
	wantIndexing := SnapshotIndexingStatistics{}
	wantFillDB := SnapshotFillDBStatistics{Stages: []SnapshotFillDBStage{{StageName: "s1", Current: 1, Total: 2}}}

	err := c.db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := SnapshotDownloadUpdater(wantDownload)(tx); err != nil {
			return err
		}
		if err := SnapshotIndexingUpdater(wantIndexing)(tx); err != nil {
			return err
		}
		return SnapshotFillDBUpdater(wantFillDB)(tx)
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	err = c.db.View(context.Background(), func(tx kv.Tx) error {
		dlData, err := SnapshotDownloadInfoFromTx(tx)
		if err != nil {
			return err
		}
		var dl SnapshotDownloadStatistics
		ParseData(dlData, &dl)
		if !reflect.DeepEqual(dl, wantDownload) {
			t.Fatalf("download = %+v, want %+v", dl, wantDownload)
		}

		idxData, err := SnapshotIndexingInfoFromTx(tx)
		if err != nil {
			return err
		}
		var idx SnapshotIndexingStatistics
		ParseData(idxData, &idx)
		if !reflect.DeepEqual(idx, wantIndexing) {
			t.Fatalf("indexing = %+v, want %+v", idx, wantIndexing)
		}

		fdbData, err := SnapshotFillDBInfoFromTx(tx)
		if err != nil {
			return err
		}
		var fdb SnapshotFillDBStatistics
		ParseData(fdbData, &fdb)
		if len(fdb.Stages) != 1 || fdb.Stages[0] != wantFillDB.Stages[0] {
			t.Fatalf("fillDB = %+v, want %+v", fdb, wantFillDB)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetFillDBInfoInsertAndUpdate(t *testing.T) {
	c := newTestClient(t)

	c.SetFillDBInfo(SnapshotFillDBStage{StageName: "a", Current: 1, Total: 10})
	if len(c.syncStats.SnapshotFillDB.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(c.syncStats.SnapshotFillDB.Stages))
	}

	// Updating the same stage name replaces in place rather than appending.
	c.SetFillDBInfo(SnapshotFillDBStage{StageName: "a", Current: 5, Total: 10})
	if len(c.syncStats.SnapshotFillDB.Stages) != 1 {
		t.Fatalf("expected still 1 stage after update, got %d", len(c.syncStats.SnapshotFillDB.Stages))
	}
	if c.syncStats.SnapshotFillDB.Stages[0].Current != 5 {
		t.Fatalf("Current = %d, want 5", c.syncStats.SnapshotFillDB.Stages[0].Current)
	}

	// DEFECT (not fixed here, per task constraints): a stage name that
	// doesn't match any existing entry is silently dropped instead of being
	// appended. SetFillDBInfo only ever appends once, in the nil-slice
	// branch taken by the very first call; every later call either updates
	// an existing StageName in place or does nothing. So once FillDB has
	// seen one stage, any *other* stage name never makes it into the list.
	c.SetFillDBInfo(SnapshotFillDBStage{StageName: "b", Current: 2, Total: 10})
	if len(c.syncStats.SnapshotFillDB.Stages) != 1 {
		t.Fatalf("len(Stages) = %d, want 1 (known bug: new stage names after the first are dropped, not appended)", len(c.syncStats.SnapshotFillDB.Stages))
	}
}

func TestSaveSnapshotStageStatsToDB(t *testing.T) {
	c := newTestClient(t)
	c.SetFillDBInfo(SnapshotFillDBStage{StageName: "s", Current: 1, Total: 1})
	c.SaveSnapshotStageStatsToDB() // must not panic or error visibly
}

func TestRunFillDBListener(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.runFillDBListener(ctx)

	waitFor(t, func() {
		Send(SnapshotFillDBStageUpdate{
			Stage:       SnapshotFillDBStage{StageName: "listener-stage", Current: 1, Total: 2},
			TimeElapsed: 1.5,
		})
	}, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, s := range c.syncStats.SnapshotFillDB.Stages {
			if s.StageName == "listener-stage" {
				return true
			}
		}
		return false
	})
}

func TestSyncStatsJsonAndSnapshotFilesListJson(t *testing.T) {
	c := newTestClient(t)
	c.syncStats.SnapshotFillDB.Stages = []SnapshotFillDBStage{{StageName: "x"}}
	c.snapshotFileList.Files = []string{"a.seg", "b.seg"}

	var buf bytes.Buffer
	c.SyncStatsJson(&buf)
	var stats SyncStatistics
	if err := json.Unmarshal(buf.Bytes(), &stats); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(stats.SnapshotFillDB.Stages) != 1 || stats.SnapshotFillDB.Stages[0].StageName != "x" {
		t.Fatalf("SyncStatsJson = %+v", stats)
	}

	var buf2 bytes.Buffer
	c.SnapshotFilesListJson(&buf2)
	var files SnapshoFilesList
	if err := json.Unmarshal(buf2.Bytes(), &files); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(files.Files) != 2 {
		t.Fatalf("SnapshotFilesListJson = %+v", files)
	}
}

func TestSyncStatisticsDeepCopy(t *testing.T) {
	c := newTestClient(t)
	c.syncStats.SnapshotFillDB.Stages = []SnapshotFillDBStage{{StageName: "orig"}}

	copy := c.SyncStatistics()
	if len(copy.SnapshotFillDB.Stages) != 1 || copy.SnapshotFillDB.Stages[0].StageName != "orig" {
		t.Fatalf("SyncStatistics() = %+v", copy)
	}

	// Mutating the copy must not affect the original (JSON round trip isolates them).
	copy.SnapshotFillDB.Stages[0].StageName = "mutated"
	if c.syncStats.SnapshotFillDB.Stages[0].StageName != "orig" {
		t.Fatal("expected original syncStats to be unaffected by mutation of the copy")
	}
}
