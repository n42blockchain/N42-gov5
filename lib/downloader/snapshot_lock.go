package downloader

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/tidwall/btree"
	"golang.org/x/sync/errgroup"

	"github.com/n42blockchain/N42/lib/chain/snapcfg"
	"github.com/n42blockchain/N42/lib/common/datadir"
	"github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/downloader/downloadercfg"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
	"github.com/n42blockchain/N42/lib/kv"
)

const SnapshotsLockFileName = "snapshot-lock.json"

type snapshotLock struct {
	Chain     string              `json:"chain"`
	Downloads snapcfg.Preverified `json:"downloads"`
}

func getSnapshotLock(ctx context.Context, cfg *downloadercfg.Cfg, db kv.RoDB, stats *AggStats, statsLock *sync.RWMutex, logger log.Logger) (*snapshotLock, error) {
	return initSnapshotLock(ctx, cfg, db, stats, statsLock, logger)
}

func initSnapshotLock(ctx context.Context, cfg *downloadercfg.Cfg, db kv.RoDB, stats *AggStats, statsLock *sync.RWMutex, logger log.Logger) (*snapshotLock, error) {
	lock := &snapshotLock{
		Chain: cfg.ChainName,
	}

	files, err := SeedableFiles(cfg.Dirs, cfg.ChainName)
	if err != nil {
		return nil, err
	}

	snapCfg := cfg.SnapshotConfig

	if snapCfg == nil {
		snapCfg = snapcfg.KnownCfg(cfg.ChainName)
	}

	//if len(files) == 0 {
	lock.Downloads = snapCfg.Preverified
	//}

	// if files exist on disk we assume that the lock file has been removed
	// or was never present so compare them against the known config to
	// recreate the lock file
	//
	// if the file is above the ExpectBlocks in the snapCfg we ignore it
	// if the file is the same version of the known file we:
	//   check if its mid upload
	//     - in which case we compare the hash in the db to the known hash
	//       - if they are different we delete the local file and include the
	//         know file in the hash which will force a re-upload
	//   otherwise
	//      - if the file has a different hash to the known file we include
	//        the files hash in the upload to preserve the local copy
	// if the file is a different version - we see if the version for the
	// file is available in know config - and if so we follow the procedure
	// above, but we use the matching version from the known config.  If there
	// is no matching version just use the one discovered for the file

	versionedCfg := map[snaptype.Version]*snapcfg.Cfg{}
	versionedCfgLock := sync.Mutex{}

	snapDir := cfg.Dirs.Snap

	var downloadMap btree.Map[string, snapcfg.PreverifiedItem]
	var downloadsMutex sync.Mutex

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(-1) * 4)
	var i atomic.Int32

	logEvery := time.NewTicker(20 * time.Second)
	defer logEvery.Stop()

	for _, file := range files {
		g.Go(func() error {
			i.Add(1)

			fileInfo, isStateFile, ok := snaptype.ParseFileName(snapDir, file)

			if !ok {
				return nil
			}

			if isStateFile {
				if preverified, ok := snapCfg.Preverified.Get(file); ok {
					downloadsMutex.Lock()
					defer downloadsMutex.Unlock()
					downloadMap.Set(file, preverified)
				}
				return nil //TODO: we don't create
			}

			if fileInfo.From > snapCfg.ExpectBlocks {
				return nil
			}

			if preverified, ok := snapCfg.Preverified.Get(fileInfo.Name()); ok {
				hashBytes, err := localHashBytes(ctx, fileInfo, db, stats, statsLock)

				if err != nil {
					return fmt.Errorf("localHashBytes: %w", err)
				}

				downloadsMutex.Lock()
				defer downloadsMutex.Unlock()

				if hash := hex.EncodeToString(hashBytes); preverified.Hash == hash {
					downloadMap.Set(fileInfo.Name(), preverified)
				} else {
					logger.Debug("[downloader] local file hash does not match known", "file", fileInfo.Name(), "local", hash, "known", preverified.Hash)
					// TODO: check if it has an index - if not use the known hash and delete the file
					downloadMap.Set(fileInfo.Name(), snapcfg.PreverifiedItem{Name: fileInfo.Name(), Hash: hash})
				}
			} else {
				versioned := func() *snapcfg.Cfg {
					versionedCfgLock.Lock()
					defer versionedCfgLock.Unlock()

					versioned, ok := versionedCfg[fileInfo.Version]

					if !ok {
						versioned = snapcfg.VersionedCfg(cfg.ChainName, fileInfo.Version, fileInfo.Version)
						versionedCfg[fileInfo.Version] = versioned
					}

					return versioned
				}()

				hashBytes, err := localHashBytes(ctx, fileInfo, db, stats, statsLock)

				if err != nil {
					return fmt.Errorf("localHashBytes: %w", err)
				}

				downloadsMutex.Lock()
				defer downloadsMutex.Unlock()

				hash := hex.EncodeToString(hashBytes)

				if preverified, ok := versioned.Preverified.Get(fileInfo.Name()); ok {
					if preverified.Hash == hash {
						downloadMap.Set(preverified.Name, preverified)
					} else {
						logger.Debug("[downloader] local file hash does not match known", "file", fileInfo.Name(), "local", hash, "known", preverified.Hash)
						downloadMap.Set(fileInfo.Name(), snapcfg.PreverifiedItem{Name: fileInfo.Name(), Hash: hash})
					}
				} else {
					downloadMap.Set(fileInfo.Name(), snapcfg.PreverifiedItem{Name: fileInfo.Name(), Hash: hash})
				}
			}

			return nil
		})
	}

	func() {
		for int(i.Load()) < len(files) {
			select {
			case <-ctx.Done():
				return // g.Wait() will return right error
			case <-logEvery.C:
				if int(i.Load()) == len(files) {
					return
				}
				log.Info("[snapshots] Initiating snapshot-lock", "progress", fmt.Sprintf("%d/%d", i.Load(), len(files)))
			}
		}
	}()

	if err := g.Wait(); err != nil {
		return nil, err
	}

	var missingItems []snapcfg.PreverifiedItem
	var downloads snapcfg.Preverified

	downloadMap.Scan(func(key string, value snapcfg.PreverifiedItem) bool {
		downloads = append(downloads, value)
		return true
	})

	for _, item := range snapCfg.Preverified {
		_, _, ok := snaptype.ParseFileName(snapDir, item.Name)
		if !ok {
			continue
		}

		if !downloads.Contains(item.Name, true) {
			missingItems = append(missingItems, item)
		}
	}

	lock.Downloads = snapcfg.Merge(downloads, missingItems)
	return lock, nil
}

func localHashBytes(ctx context.Context, fileInfo snaptype.FileInfo, db kv.RoDB, stats *AggStats, statsLock *sync.RWMutex) ([]byte, error) {
	var hashBytes []byte

	if db != nil {
		err := db.View(ctx, func(tx kv.Tx) (err error) {
			infoBytes, err := tx.GetOne(kv.BittorrentInfo, []byte(fileInfo.Name()))

			if err != nil {
				return err
			}

			if len(infoBytes) == 20 {
				hashBytes = infoBytes
				return nil
			}

			var info torrentInfo

			if err = json.Unmarshal(infoBytes, &info); err == nil {
				hashBytes = info.Hash
			}

			return nil
		})

		if err != nil {
			return nil, err
		}
	}

	if len(hashBytes) != 0 {
		return hashBytes, nil
	}

	meta, err := metainfo.LoadFromFile(fileInfo.Path + ".torrent")

	if err == nil {
		if spec, err := torrent.TorrentSpecFromMetaInfoErr(meta); err == nil {
			return spec.InfoHash.Bytes(), nil
		}
	}

	return fileHashBytes(ctx, fileInfo, stats, statsLock)
}

func fileHashBytes(ctx context.Context, fileInfo snaptype.FileInfo, stats *AggStats, statsLock *sync.RWMutex) ([]byte, error) {

	if !dir.FileExist(fileInfo.Path) {
		return nil, os.ErrNotExist
	}

	defer func(t time.Time) {
		statsLock.Lock()
		defer statsLock.Unlock()
		stats.LocalFileHashes++
		stats.LocalFileHashTime += time.Since(t)
	}(time.Now())

	info := &metainfo.Info{PieceLength: downloadercfg.DefaultPieceSize, Name: fileInfo.Name()}

	if err := info.BuildFromFilePath(fileInfo.Path); err != nil {
		return nil, fmt.Errorf("can't get local hash for %s: %w", fileInfo.Name(), err)
	}

	meta, err := CreateMetaInfo(info, nil)

	if err != nil {
		return nil, fmt.Errorf("can't get local hash for %s: %w", fileInfo.Name(), err)
	}

	spec, err := torrent.TorrentSpecFromMetaInfoErr(meta)

	if err != nil {
		return nil, fmt.Errorf("can't get local hash for %s: %w", fileInfo.Name(), err)
	}

	return spec.InfoHash.Bytes(), nil
}

func SeedableFiles(dirs datadir.Dirs, chainName string) ([]string, error) {
	files, err := seedableSegmentFiles(dirs.Snap, chainName)
	if err != nil {
		return nil, fmt.Errorf("seedableSegmentFiles: %w", err)
	}
	for _, subDir := range []string{"idx", "history", "domain"} {
		subFiles, err := seedableStateFilesBySubDir(dirs.Snap, subDir)
		if err != nil {
			return nil, err
		}
		files = append(files, subFiles...)
	}
	return files, nil
}
