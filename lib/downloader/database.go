package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"
	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/log/v3"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/downloader/downloadercfg"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
)

func openClient(ctx context.Context, dbDir, snapDir string, cfg *torrent.ClientConfig) (db kv.RwDB, c storage.PieceCompletion, m storage.ClientImplCloser, torrentClient *torrent.Client, err error) {
	db, err = mdbx.NewMDBX(log.New()).
		Label(kv.DownloaderDB).
		WithTableCfg(func(defaultBuckets kv.TableCfg) kv.TableCfg { return kv.DownloaderTablesCfg }).
		GrowthStep(16 * datasize.MB).
		MapSize(16 * datasize.GB).
		PageSize(uint64(4 * datasize.KB)).
		//WriteMap().
		//LifoReclaim().
		RoTxsLimiter(semaphore.NewWeighted(9_000)).
		Path(dbDir).
		Open(ctx)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("torrentcfg.openClient: %w", err)
	}
	//c, err = NewMdbxPieceCompletion(db)
	c, err = NewMdbxPieceCompletionBatch(db)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("torrentcfg.NewMdbxPieceCompletion: %w", err)
	}

	// Use file-based storage instead of MMAP to avoid data loss on shutdown
	// MMAP can lose data if msync is not called before close
	// File-based storage is safer as it syncs data to disk on each write
	// See also: https://github.com/erigontech/erigon/pull/10074
	//m = storage.NewMMapWithCompletion(snapDir, c)
	m = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   snapDir,
		PieceCompletion: c,
	})
	cfg.DefaultStorage = m

	dnsResolver := &downloadercfg.DnsCacheResolver{RefreshTimeout: 24 * time.Hour}
	cfg.TrackerDialContext = dnsResolver.DialContext

	torrentClient, err = torrent.NewClient(cfg)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("torrent.NewClient: %w", err)
	}

	go func() {
		dnsResolver.Run(ctx)
	}()

	return db, c, m, torrentClient, nil
}

func (d *Downloader) torrentInfo(name string) (*torrentInfo, error) {
	var info torrentInfo

	err := d.db.View(d.ctx, func(tx kv.Tx) (err error) {
		infoBytes, err := tx.GetOne(kv.BittorrentInfo, []byte(name))

		if err != nil {
			return err
		}

		if err = json.Unmarshal(infoBytes, &info); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &info, nil
}
