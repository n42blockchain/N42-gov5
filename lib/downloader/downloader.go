/*
   Copyright 2021 Erigon contributors

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

package downloader

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/n42blockchain/N42/lib/log/v3"
	"golang.org/x/time/rate"

	"github.com/n42blockchain/N42/lib/downloader/downloadercfg"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
	"github.com/n42blockchain/N42/lib/kv"
)

// Downloader - component which downloading historical files. Can use BitTorrent, or other protocols
type Downloader struct {
	db                  kv.RwDB
	pieceCompletionDB   storage.PieceCompletion
	torrentClient       *torrent.Client
	webDownloadClient   *RCloneClient
	webDownloadSessions map[string]*RCloneSession

	cfg *downloadercfg.Cfg

	lock  *sync.RWMutex
	stats AggStats

	folder storage.ClientImplCloser

	ctx          context.Context
	stopMainLoop context.CancelFunc
	wg           sync.WaitGroup

	webseeds         *WebSeeds
	webseedsDiscover bool

	logger    log.Logger
	verbosity log.Lvl

	torrentFS       *AtomicTorrentFS
	snapshotLock    *snapshotLock
	webDownloadInfo map[string]webDownloadInfo
	downloading     map[string]struct{}
	downloadLimit   *rate.Limit
}

func New(ctx context.Context, cfg *downloadercfg.Cfg, logger log.Logger, verbosity log.Lvl, discover bool) (*Downloader, error) {
	requestHandler := &requestHandler{
		Transport: http.Transport{
			Proxy:       cfg.ClientConfig.HTTPProxy,
			DialContext: cfg.ClientConfig.HTTPDialContext,
			// I think this value was observed from some webseeds. It seems reasonable to extend it
			// to other uses of HTTP from the client.
			MaxConnsPerHost: 10,
		}}

	cfg.ClientConfig.WebTransport = requestHandler

	db, c, m, torrentClient, err := openClient(ctx, cfg.Dirs.Downloader, cfg.Dirs.Snap, cfg.ClientConfig)
	if err != nil {
		return nil, fmt.Errorf("openClient: %w", err)
	}

	peerID, err := readPeerID(db)
	if err != nil {
		return nil, fmt.Errorf("get peer id: %w", err)
	}
	cfg.ClientConfig.PeerID = string(peerID)
	if len(peerID) == 0 {
		if err = savePeerID(db, torrentClient.PeerID()); err != nil {
			return nil, fmt.Errorf("save peer id: %w", err)
		}
	}

	mutex := &sync.RWMutex{}
	stats := AggStats{
		WebseedTripCount:     &atomic.Int64{},
		WebseedBytesDownload: &atomic.Int64{},
		WebseedDiscardCount:  &atomic.Int64{},
		WebseedServerFails:   &atomic.Int64{},
		downloadProgress:     map[string]downloadProgress{},
	}

	lock, err := getSnapshotLock(ctx, cfg, db, &stats, mutex, logger)
	if err != nil {
		return nil, fmt.Errorf("can't initialize snapshot lock: %w", err)
	}

	d := &Downloader{
		cfg:                 cfg,
		db:                  db,
		pieceCompletionDB:   c,
		folder:              m,
		torrentClient:       torrentClient,
		lock:                mutex,
		stats:               stats,
		webseeds:            NewWebSeeds(cfg.WebSeedUrls, verbosity, logger),
		logger:              logger,
		verbosity:           verbosity,
		torrentFS:           &AtomicTorrentFS{dir: cfg.Dirs.Snap},
		snapshotLock:        lock,
		webDownloadInfo:     map[string]webDownloadInfo{},
		webDownloadSessions: map[string]*RCloneSession{},
		downloading:         map[string]struct{}{},
		webseedsDiscover:    discover,
	}
	d.webseeds.SetTorrent(d.torrentFS, lock.Downloads, cfg.DownloadTorrentFilesFromWebseed)

	requestHandler.downloader = d

	if cfg.ClientConfig.DownloadRateLimiter != nil {
		downloadLimit := cfg.ClientConfig.DownloadRateLimiter.Limit()
		d.downloadLimit = &downloadLimit
	}

	d.ctx, d.stopMainLoop = context.WithCancel(ctx)

	if cfg.AddTorrentsFromDisk {
		for _, download := range lock.Downloads {
			if info, err := d.torrentInfo(download.Name); err == nil {
				if info.Completed != nil {
					if hash := hex.EncodeToString(info.Hash); download.Hash != hash {
						fileInfo, _, ok := snaptype.ParseFileName(d.SnapDir(), download.Name)

						if !ok {
							d.logger.Debug("[snapshots] Can't parse download filename", "file", download.Name)
							continue
						}

						// this is lazy as it can be expensive for large files
						fileHashBytes, err := fileHashBytes(d.ctx, fileInfo, &d.stats, d.lock)

						if errors.Is(err, os.ErrNotExist) {
							hashBytes, _ := hex.DecodeString(download.Hash)
							if err := d.db.Update(d.ctx, torrentInfoReset(download.Name, hashBytes, 0)); err != nil {
								d.logger.Debug("[snapshots] Can't update torrent info", "file", download.Name, "hash", download.Hash, "err", err)
							}
							continue
						}

						fileHash := hex.EncodeToString(fileHashBytes)

						if fileHash != download.Hash && fileHash != hash {
							d.logger.Debug("[snapshots] download db mismatch", "file", download.Name, "lock", download.Hash, "db", hash, "disk", fileHash, "downloaded", *info.Completed)
						} else {
							d.logger.Debug("[snapshots] lock hash does not match completed download", "file", download.Name, "lock", hash, "download", download.Hash, "downloaded", *info.Completed)
						}
					}
				}
			}
		}

		if err := d.BuildTorrentFilesIfNeed(d.ctx, lock.Chain, lock.Downloads); err != nil {
			return nil, err
		}

		if err := d.addTorrentFilesFromDisk(false); err != nil {
			return nil, err
		}
	}

	return d, nil
}

func (d *Downloader) Close() {
	d.logger.Debug("[snapshots] stopping downloader")
	d.stopMainLoop()
	d.wg.Wait()
	d.logger.Debug("[snapshots] closing torrents")
	d.torrentClient.Close()
	if err := d.folder.Close(); err != nil {
		d.logger.Warn("[snapshots] folder.close", "err", err)
	}
	if err := d.pieceCompletionDB.Close(); err != nil {
		d.logger.Warn("[snapshots] pieceCompletionDB.close", "err", err)
	}
	d.db.Close()
	d.logger.Debug("[snapshots] downloader stopped")
}

func (d *Downloader) Stats() AggStats {
	d.lock.RLock()
	defer d.lock.RUnlock()
	return d.stats
}

func (d *Downloader) SnapDir() string { return d.cfg.Dirs.Snap }

func (d *Downloader) TorrentClient() *torrent.Client { return d.torrentClient }

func (d *Downloader) PeerID() []byte {
	peerID := d.torrentClient.PeerID()
	return peerID[:]
}

func (d *Downloader) StopSeeding(hash metainfo.Hash) error {
	t, ok := d.torrentClient.Torrent(hash)
	if !ok {
		return nil
	}
	ch := t.Closed()
	t.Drop()
	<-ch
	return nil
}
