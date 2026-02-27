package downloader

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/types/infohash"
	"github.com/n42blockchain/N42/lib/log/v3"
	"golang.org/x/sync/errgroup"

	"github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/downloader/downloadercfg"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
)

func (d *Downloader) checkComplete(name string) (bool, int64, *time.Time) {
	info, err := d.torrentInfo(name)
	if err != nil {
		return false, 0, nil
	}
	if info.Completed == nil || !info.Completed.Before(time.Now()) || info.Length == nil {
		return false, 0, nil
	}
	fi, err := os.Stat(filepath.Join(d.SnapDir(), name))
	if err != nil {
		return false, 0, nil
	}
	return fi.Size() == *info.Length && fi.ModTime().Equal(*info.Completed), *info.Length, info.Completed
}

func localHashCompletionCheck(ctx context.Context, t *torrent.Torrent, fileInfo snaptype.FileInfo, statusChan chan downloadStatus, stats *AggStats, statsLock *sync.RWMutex) ([]byte, bool) {
	localHash, err := fileHashBytes(ctx, fileInfo, stats, statsLock)
	if err != nil {
		return localHash, false
	}
	if !bytes.Equal(t.InfoHash().Bytes(), localHash) {
		return localHash, false
	}
	statusChan <- downloadStatus{
		name:     t.Name(),
		length:   t.Length(),
		infoHash: t.InfoHash(),
	}
	return localHash, true
}

func logSeedHashMismatches(torrentHash infohash.T, name string, seedHashMismatches map[infohash.T][]*seedHash, logger log.Logger) {
	var nohash []*seedHash
	var mismatch []*seedHash

	for _, entry := range seedHashMismatches[torrentHash] {
		if entry.reported {
			continue
		}
		entry.reported = true
		if entry.hash == nil {
			nohash = append(nohash, entry)
		} else {
			mismatch = append(mismatch, entry)
		}
	}

	if len(nohash) > 0 {
		var webseeds string
		for _, entry := range nohash {
			if len(webseeds) > 0 {
				webseeds += ", "
			}
			webseeds += strings.TrimSuffix(entry.url.String(), "/")
		}
		logger.Warn("No webseed entry for torrent", "name", name, "hash", torrentHash.HexString(), "webseeds", webseeds)
	}

	if len(mismatch) > 0 {
		var webseeds string
		for _, entry := range mismatch {
			if len(webseeds) > 0 {
				webseeds += ", "
			}
			webseeds += strings.TrimSuffix(entry.url.String(), "/") + "#" + entry.hash.HexString()
		}
		logger.Warn("Webseed hash mismatch for torrent", "name", name, "hash", torrentHash.HexString(), "webseeds", webseeds)
	}
}

func (d *Downloader) VerifyData(ctx context.Context, whiteList []string, failFast bool) error {
	total := 0
	allTorrents := d.torrentClient.Torrents()
	toVerify := make([]*torrent.Torrent, 0, len(allTorrents))

	for _, t := range allTorrents {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.GotInfo():
		default:
			continue
		}

		if !dir.FileExist(filepath.Join(d.SnapDir(), t.Name())) {
			continue
		}

		if len(whiteList) > 0 {
			name := t.Name()
			exactOrPartialMatch := slices.ContainsFunc(whiteList, func(s string) bool {
				return name == s || strings.HasSuffix(name, s) || strings.HasPrefix(name, s)
			})
			if !exactOrPartialMatch {
				continue
			}
		}
		toVerify = append(toVerify, t)
		total += t.NumPieces()
	}

	d.logger.Info("[snapshots] Verify start")
	defer d.logger.Info("[snapshots] Verify done", "files", len(toVerify), "whiteList", whiteList)

	completedPieces, completedFiles := &atomic.Uint64{}, &atomic.Uint64{}

	logEvery := time.NewTicker(20 * time.Second)
	defer logEvery.Stop()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-logEvery.C:
				d.logger.Info("[snapshots] Verify",
					"progress", fmt.Sprintf("%.2f%%", 100*float64(completedPieces.Load())/float64(total)),
					"files", fmt.Sprintf("%d/%d", completedFiles.Load(), len(toVerify)),
					"sz_gb", downloadercfg.DefaultPieceSize*completedPieces.Load()/1024/1024/1024,
				)
			}
		}
	}()

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(-1) * 4)
	for _, t := range toVerify {
		g.Go(func() error {
			defer completedFiles.Add(1)
			if failFast {
				return VerifyFileFailFast(ctx, t, d.SnapDir(), completedPieces)
			}

			err := ScheduleVerifyFile(ctx, t, completedPieces)
			if err != nil || !t.Complete.Bool() {
				if dbErr := d.db.Update(ctx, torrentInfoReset(t.Name(), t.InfoHash().Bytes(), 0)); dbErr != nil {
					return fmt.Errorf("verify data: %s: reset failed: %w", t.Name(), dbErr)
				}
			}
			return err
		})
	}
	return g.Wait()
}
