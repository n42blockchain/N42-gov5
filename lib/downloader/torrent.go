package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/n42blockchain/N42/lib/log/v3"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/chain/snapcfg"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
)

const ParallelVerifyFiles = 4 // keep it small, to allow big `PieceHashersPerTorrent`. More `PieceHashersPerTorrent` - faster handling of big files.

func (d *Downloader) addTorrentFilesFromDisk(quiet bool) error {
	logEvery := time.NewTicker(20 * time.Second)
	defer logEvery.Stop()

	eg, ctx := errgroup.WithContext(d.ctx)
	eg.SetLimit(ParallelVerifyFiles)

	files, err := AllTorrentSpecs(d.cfg.Dirs, d.torrentFS)
	if err != nil {
		return err
	}

	// reduce mutex contention inside torrentClient - by enabling in/out peers connection after addig all files
	for _, ts := range files {
		ts.Trackers = nil
		ts.DisallowDataDownload = true
	}
	defer func() {
		tl := d.torrentClient.Torrents()
		for _, t := range tl {
			t.AllowDataUpload()
			t.AddTrackers(Trackers)
		}
	}()

	for i, ts := range files {
		d.lock.RLock()
		_, downloading := d.downloading[ts.DisplayName]
		d.lock.RUnlock()

		if downloading {
			continue
		}

		// this check is performed here becuase t.MergeSpec in addTorrentFile will do a file
		// update in place when it opens its MemMap.  This is non destructive for the data
		// but casues an update to the file which changes its size to the torrent length which
		// invalidated the file length check
		if info, err := d.torrentInfo(ts.DisplayName); err == nil {
			if info.Completed != nil {
				fi, serr := os.Stat(filepath.Join(d.SnapDir(), info.Name))
				if serr != nil || fi.Size() != *info.Length || !fi.ModTime().Equal(*info.Completed) {
					if err := d.db.Update(d.ctx, torrentInfoReset(info.Name, info.Hash, *info.Length)); err != nil {
						if serr != nil {
							log.Error("[snapshots] Failed to reset db entry after stat error", "file", info.Name, "err", err, "stat-err", serr)
						} else {
							log.Error("[snapshots] Failed to reset db entry after stat mismatch", "file", info.Name, "err", err)
						}
					}
				}
			}
		}

		if whitelisted, ok := d.webseeds.torrentsWhitelist.Get(ts.DisplayName); ok {
			if ts.InfoHash.HexString() != whitelisted.Hash {
				continue
			}
		}

		eg.Go(func() error {
			_, _, err := addTorrentFile(ctx, ts, d.torrentClient, d.db, d.webseeds)
			if err != nil {
				return err
			}
			select {
			case <-logEvery.C:
				if !quiet {
					log.Info("[snapshots] Adding .torrent files", "progress", fmt.Sprintf("%d/%d", i, len(files)))
				}
			default:
			}
			return nil
		})
	}
	return eg.Wait()
}

func (d *Downloader) BuildTorrentFilesIfNeed(ctx context.Context, chain string, ignore snapcfg.Preverified) error {
	_, err := BuildTorrentFilesIfNeed(ctx, d.cfg.Dirs, d.torrentFS, chain, ignore)
	return err
}

// AddNewSeedableFile decides what we do depending on wether we have the .seg file or the .torrent file
// have .torrent no .seg => get .seg file from .torrent
// have .seg no .torrent => get .torrent from .seg
func (d *Downloader) AddNewSeedableFile(ctx context.Context, name string) error {
	ff, isStateFile, ok := snaptype.ParseFileName("", name)
	if ok {
		if isStateFile {
			if !snaptype.E3Seedable(name) {
				return nil
			}
		} else {
			if !d.cfg.SnapshotConfig.Seedable(ff) {
				return nil
			}
		}
	}

	// if we don't have the torrent file we build it if we have the .seg file
	_, err := BuildTorrentIfNeed(ctx, name, d.SnapDir(), d.torrentFS)
	if err != nil {
		return fmt.Errorf("AddNewSeedableFile: %w", err)
	}
	ts, err := d.torrentFS.LoadByName(name)
	if err != nil {
		return fmt.Errorf("AddNewSeedableFile: %w", err)
	}
	_, _, err = addTorrentFile(ctx, ts, d.torrentClient, d.db, d.webseeds)
	if err != nil {
		return fmt.Errorf("addTorrentFile: %w", err)
	}
	return nil
}

func (d *Downloader) alreadyHaveThisName(name string) bool {
	for _, t := range d.torrentClient.Torrents() {
		if t.Info() != nil {
			if t.Name() == name {
				return true
			}
		}
	}
	return false
}

func (d *Downloader) AddMagnetLink(ctx context.Context, infoHash metainfo.Hash, name string) error {
	// Paranoic Mode on: if same file changed infoHash - skip it
	// Example:
	//  - Erigon generated file X with hash H1. User upgraded Erigon. New version has preverified file X with hash H2. Must ignore H2 (don't send to Downloader)
	if d.alreadyHaveThisName(name) || !IsSnapNameAllowed(name) {
		return nil
	}
	isProhibited, err := d.torrentFS.NewDownloadsAreProhibited(name)
	if err != nil {
		return err
	}

	if isProhibited && !d.torrentFS.Exists(name) {
		return nil
	}

	mi := &metainfo.MetaInfo{AnnounceList: Trackers}
	magnet := mi.Magnet(&infoHash, &metainfo.Info{Name: name})
	spec, err := torrent.TorrentSpecFromMagnetUri(magnet.String())

	if err != nil {
		return err
	}

	t, ok, err := addTorrentFile(ctx, spec, d.torrentClient, d.db, d.webseeds)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	d.wg.Add(1)
	go func(t *torrent.Torrent) {
		defer d.wg.Done()
		select {
		case <-ctx.Done():
			return
		case <-t.GotInfo():
		case <-time.After(30 * time.Second): //fallback to r2
			// TOOD: handle errors
			// TOOD: add `d.webseeds.Complete` chan - to prevent race - Discover is also async
			// TOOD: maybe run it in goroutine and return channel - to select with p2p

			ts, ok, err := d.webseeds.DownloadAndSaveTorrentFile(ctx, name)
			if ok && err == nil {
				_, _, err = addTorrentFile(ctx, ts, d.torrentClient, d.db, d.webseeds)
				if err != nil {
					return
				}
				return
			}

			// wait for p2p
			select {
			case <-ctx.Done():
				return
			case <-t.GotInfo():
			}
		}

		mi := t.Metainfo()
		if _, err := d.torrentFS.CreateWithMetaInfo(t.Info(), &mi); err != nil {
			d.logger.Warn("[snapshots] create torrent file", "err", err)
			return
		}

		urls, ok := d.webseeds.ByFileName(t.Name())
		if ok {
			t.AddWebSeeds(urls)
		}
	}(t)
	//log.Debug("[downloader] downloaded both seg and torrent files", "hash", infoHash)
	return nil
}

func (d *Downloader) torrentDownload(t *torrent.Torrent, statusChan chan downloadStatus, sem *semaphore.Weighted) {

	d.lock.Lock()
	d.downloading[t.Name()] = struct{}{}
	d.lock.Unlock()

	if err := sem.Acquire(d.ctx, 1); err != nil {
		d.logger.Warn("Failed to acquire download semaphore", "err", err)
		return
	}

	d.wg.Add(1)

	go func(t *torrent.Torrent) {
		defer d.wg.Done()
		defer sem.Release(1)

		t.AllowDataDownload()

		select {
		case <-d.ctx.Done():
			return
		case <-t.GotInfo():
		}

		t.DownloadAll()

		idleCount := 0
		var lastRead int64

		for {
			select {
			case <-d.ctx.Done():
				return
			case <-t.Complete.On():
				return
			case <-time.After(10 * time.Second):
				bytesRead := t.Stats().BytesReadData

				if lastRead-bytesRead.Int64() == 0 {
					idleCount++
				} else {
					lastRead = bytesRead.Int64()
					idleCount = 0
				}

				//fallback to webDownloadClient, but only if it's enabled
				if d.webDownloadClient != nil && idleCount > 6 {
					t.DisallowDataDownload()
					return
				}
			}
		}
	}(t)
}

func availableTorrents(ctx context.Context, pending []*torrent.Torrent, slots int) []*torrent.Torrent {
	if slots == 0 {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
			return nil
		}
	}

	slices.SortFunc(pending, func(i, j *torrent.Torrent) int {
		in, _, _ := snaptype.ParseFileName("", i.Name())
		jn, _, _ := snaptype.ParseFileName("", j.Name())
		return in.CompareTo(jn)
	})

	var available []*torrent.Torrent

	for len(pending) > 0 && pending[0].Info() != nil {
		available = append(available, pending[0])

		if len(available) == slots {
			return available
		}

		pending = pending[1:]
	}

	if len(pending) == 0 {
		return available
	}

	cases := make([]reflect.SelectCase, 0, len(pending)+2)

	for _, t := range pending {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(t.GotInfo()),
		})
	}

	if len(cases) == 0 {
		return nil
	}

	cases = append(cases, reflect.SelectCase{
		Dir:  reflect.SelectRecv,
		Chan: reflect.ValueOf(ctx.Done()),
	},
		reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(time.After(10 * time.Second)),
		})

	for {
		selected, _, _ := reflect.Select(cases)

		switch selected {
		case len(cases) - 2:
			return nil
		case len(cases) - 1:
			return available
		default:
			available = append(available, pending[selected])

			if len(available) == slots {
				return available
			}

			pending = append(pending[:selected], pending[selected+1:]...)
			cases = append(cases[:selected], cases[selected+1:]...)
		}
	}
}
