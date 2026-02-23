package downloader

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/types/infohash"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
)

func (d *Downloader) MainLoopInBackground(silent bool) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if err := d.mainLoop(silent); err != nil {
			if !errors.Is(err, context.Canceled) {
				d.logger.Warn("[snapshots]", "err", err)
			}
		}
	}()
}

func (d *Downloader) mainLoop(silent bool) error {
	if d.webseedsDiscover {
		// CornerCase: no peers -> no anoncments to trackers -> no magnetlink resolution (but magnetlink has filename)
		// means we can start adding weebseeds without waiting for `<-t.GotInfo()`
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			// webseeds.Discover may create new .torrent files on disk
			d.webseeds.Discover(d.ctx, d.cfg.WebSeedFiles, d.cfg.Dirs.Snap)
			// apply webseeds to existing torrents
			if err := d.addTorrentFilesFromDisk(true); err != nil && !errors.Is(err, context.Canceled) {
				d.logger.Warn("[snapshots] addTorrentFilesFromDisk", "err", err)
			}

			d.lock.Lock()
			defer d.lock.Unlock()

			for _, t := range d.torrentClient.Torrents() {
				if urls, ok := d.webseeds.ByFileName(t.Name()); ok {
					// if we have created a torrent, but it has no info, assume that the
					// webseed download either has not been called yet or has failed and
					// try again here - otherwise the torrent will be left with no info
					if t.Info() == nil {
						ts, ok, err := d.webseeds.DownloadAndSaveTorrentFile(d.ctx, t.Name())
						if ok && err == nil {
							_, _, err = addTorrentFile(d.ctx, ts, d.torrentClient, d.db, d.webseeds)
							if err != nil {
								continue
							}
						}
					}

					t.AddWebSeeds(urls)
				}
			}
		}()
	}

	var sem = semaphore.NewWeighted(int64(d.cfg.DownloadSlots))

	//TODO: feature is not ready yet
	//d.webDownloadClient, _ = NewRCloneClient(d.logger)
	d.webDownloadClient = nil

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		complete := map[string]struct{}{}
		checking := map[string]struct{}{}
		failed := map[string]struct{}{}
		waiting := map[string]struct{}{}

		downloadComplete := make(chan downloadStatus, 100)
		seedHashMismatches := map[infohash.T][]*seedHash{}

		// set limit here to make load predictable, not to control Disk/CPU consumption
		// will impact start times depending on the amount of non complete files - should
		// be low unless the download db is deleted - in which case all files may be checked
		checkGroup, _ := errgroup.WithContext(d.ctx)
		checkGroup.SetLimit(runtime.GOMAXPROCS(-1) * 4)

		for {
			torrents := d.torrentClient.Torrents()

			var pending []*torrent.Torrent

			for _, t := range torrents {
				if _, ok := complete[t.Name()]; ok {
					continue
				}

				if isComplete, length, completionTime := d.checkComplete(t.Name()); isComplete && completionTime != nil {
					if _, ok := checking[t.Name()]; !ok {
						fileInfo, _, ok := snaptype.ParseFileName(d.SnapDir(), t.Name())

						if !ok {
							downloadComplete <- downloadStatus{
								name: fileInfo.Name(),
								err:  fmt.Errorf("can't parse file name: %s", fileInfo.Name()),
							}
						}

						stat, err := os.Stat(fileInfo.Path)

						if err != nil {
							downloadComplete <- downloadStatus{
								name: fileInfo.Name(),
								err:  err,
							}
						}

						if completionTime != nil {
							if !stat.ModTime().Equal(*completionTime) {
								checking[t.Name()] = struct{}{}

								go func(fileInfo snaptype.FileInfo, infoHash infohash.T, length int64, completionTime time.Time) {
									checkGroup.Go(func() error {
										fileHashBytes, _ := fileHashBytes(d.ctx, fileInfo, &d.stats, d.lock)

										if bytes.Equal(infoHash.Bytes(), fileHashBytes) {
											downloadComplete <- downloadStatus{
												name:     fileInfo.Name(),
												length:   length,
												infoHash: infoHash,
											}
										} else {
											downloadComplete <- downloadStatus{
												name: fileInfo.Name(),
												err:  fmt.Errorf("hash check failed"),
											}

											d.logger.Warn("[snapshots] Torrent hash does not match file", "file", fileInfo.Name(), "torrent-hash", infoHash, "file-hash", hex.EncodeToString(fileHashBytes))
										}

										return nil
									})
								}(fileInfo, t.InfoHash(), length, *completionTime)

							} else {
								complete[t.Name()] = struct{}{}
								continue
							}
						}
					}
				} else {
					delete(failed, t.Name())
				}

				if _, ok := failed[t.Name()]; ok {
					continue
				}

				d.lock.RLock()
				_, downloading := d.downloading[t.Name()]
				d.lock.RUnlock()

				if downloading && t.Complete.Bool() {
					select {
					case <-d.ctx.Done():
						return
					case <-t.GotInfo():
					}

					var completionTime *time.Time
					fileInfo, _, ok := snaptype.ParseFileName(d.SnapDir(), t.Name())

					if !ok {
						d.logger.Debug("[snapshots] Can't parse downloaded filename", "file", t.Name())
						failed[t.Name()] = struct{}{}
						continue
					}

					info, err := d.torrentInfo(t.Name())

					if err == nil {
						completionTime = info.Completed
					}

					if completionTime == nil {
						now := time.Now()
						completionTime = &now
					}

					if statInfo, _ := os.Stat(fileInfo.Path); statInfo != nil {
						if !statInfo.ModTime().Equal(*completionTime) {
							os.Chtimes(fileInfo.Path, time.Time{}, *completionTime)
						}

						if statInfo, _ := os.Stat(fileInfo.Path); statInfo != nil {
							// round completion time to os granularity
							modTime := statInfo.ModTime()
							completionTime = &modTime
						}
					}

					if err := d.db.Update(d.ctx,
						torrentInfoUpdater(t.Info().Name, nil, t.Info().Length, completionTime)); err != nil {
						d.logger.Warn("[snapshots] Failed to update file info", "file", t.Info().Name, "err", err)
					}

					d.lock.Lock()
					delete(d.downloading, t.Name())
					d.lock.Unlock()
					complete[t.Name()] = struct{}{}
					continue
				}

				if downloading {
					continue
				}

				pending = append(pending, t)
			}

			select {
			case <-d.ctx.Done():
				return
			case status := <-downloadComplete:
				d.lock.Lock()
				delete(d.downloading, status.name)
				d.lock.Unlock()

				delete(checking, status.name)

				if status.spec != nil {
					_, _, err := d.torrentClient.AddTorrentSpec(status.spec)

					if err != nil {
						d.logger.Warn("Can't re-add spec after download", "file", status.name, "err", err)
					}

				}

				if status.err == nil {
					var completionTime *time.Time
					fileInfo, _, ok := snaptype.ParseFileName(d.SnapDir(), status.name)

					if !ok {
						d.logger.Debug("[snapshots] Can't parse downloaded filename", "file", status.name)
						continue
					}

					if info, err := d.torrentInfo(status.name); err == nil {
						completionTime = info.Completed
					}

					if completionTime == nil {
						now := time.Now()
						completionTime = &now
					}

					if statInfo, _ := os.Stat(fileInfo.Path); statInfo != nil {
						if !statInfo.ModTime().Equal(*completionTime) {
							os.Chtimes(fileInfo.Path, time.Time{}, *completionTime)
						}

						if statInfo, _ := os.Stat(fileInfo.Path); statInfo != nil {
							// round completion time to os granularity
							modTime := statInfo.ModTime()
							completionTime = &modTime
						}
					}

					if err := d.db.Update(context.Background(),
						torrentInfoUpdater(status.name, status.infoHash.Bytes(), status.length, completionTime)); err != nil {
						d.logger.Warn("[snapshots] Failed to update file info", "file", status.name, "err", err)
					}

					complete[status.name] = struct{}{}
					continue
				} else {
					delete(complete, status.name)
				}

			default:
			}

			d.lock.RLock()
			webDownloadInfoLen := len(d.webDownloadInfo)
			d.lock.RUnlock()

			if len(pending)+webDownloadInfoLen == 0 {
				select {
				case <-d.ctx.Done():
					return
				case <-time.After(10 * time.Second):
					continue
				}
			}

			d.lock.RLock()
			downloadingLen := len(d.downloading)
			d.stats.Downloading = int32(downloadingLen)
			d.lock.RUnlock()

			available := availableTorrents(d.ctx, pending, d.cfg.DownloadSlots-downloadingLen)

			d.lock.RLock()
			for _, webDownload := range d.webDownloadInfo {
				_, downloading := d.downloading[webDownload.torrent.Name()]

				if downloading {
					continue
				}

				addDownload := true

				for _, t := range available {
					if t.Name() == webDownload.torrent.Name() {
						addDownload = false
						break
					}
				}

				if addDownload {
					if len(available) < d.cfg.DownloadSlots-downloadingLen {
						available = append(available, webDownload.torrent)
					}
				} else {
					if wi, _, ok := snaptype.ParseFileName(d.SnapDir(), webDownload.torrent.Name()); ok {
						for i, t := range available {
							if ai, _, ok := snaptype.ParseFileName(d.SnapDir(), t.Name()); ok {
								if ai.CompareTo(wi) > 0 {
									available[i] = webDownload.torrent
									break
								}
							}
						}
					}
				}
			}
			d.lock.RUnlock()

			for _, t := range available {

				torrentInfo, err := d.torrentInfo(t.Name())

				if err != nil {
					if err := d.db.Update(d.ctx, torrentInfoReset(t.Name(), t.InfoHash().Bytes(), 0)); err != nil {
						d.logger.Debug("[snapshots] Can't update torrent info", "file", t.Name(), "hash", t.InfoHash(), "err", err)
					}
				}

				fileInfo, _, ok := snaptype.ParseFileName(d.SnapDir(), t.Name())

				if !ok {
					d.logger.Debug("[snapshots] Can't parse download filename", "file", t.Name())
					failed[t.Name()] = struct{}{}
					continue
				}

				if torrentInfo != nil {
					if torrentInfo.Completed != nil {
						// is the last completed download for this file is the same as the current torrent
						// check if we can re-use the existing file rather than re-downloading it
						if bytes.Equal(t.InfoHash().Bytes(), torrentInfo.Hash) {
							// has the local file changed since we downloaded it - if it has just download it otherwise
							// do a hash check as if we already have the file - we don't need to download it again
							if fi, err := os.Stat(filepath.Join(d.SnapDir(), t.Name())); err == nil && fi.ModTime().Equal(*torrentInfo.Completed) {
								localHash, complete := localHashCompletionCheck(d.ctx, t, fileInfo, downloadComplete, &d.stats, d.lock)

								if complete {
									d.logger.Trace("[snapshots] Ignoring download request - already complete", "file", t.Name(), "hash", t.InfoHash())
									continue
								}

								failed[t.Name()] = struct{}{}
								d.logger.Debug("[snapshots] NonCanonical hash", "file", t.Name(), "got", hex.EncodeToString(localHash), "expected", t.InfoHash(), "downloaded", *torrentInfo.Completed)
								continue
							} else {
								if err := d.db.Update(d.ctx, torrentInfoReset(t.Name(), t.InfoHash().Bytes(), 0)); err != nil {
									d.logger.Debug("[snapshots] Can't reset torrent info", "file", t.Name(), "hash", t.InfoHash(), "err", err)
								}
							}
						} else {
							if err := d.db.Update(d.ctx, torrentInfoReset(t.Name(), t.InfoHash().Bytes(), 0)); err != nil {
								d.logger.Debug("[snapshots] Can't update torrent info", "file", t.Name(), "hash", t.InfoHash(), "err", err)
							}

							if _, complete := localHashCompletionCheck(d.ctx, t, fileInfo, downloadComplete, &d.stats, d.lock); complete {
								d.logger.Trace("[snapshots] Ignoring download request - already complete", "file", t.Name(), "hash", t.InfoHash())
								continue
							}
						}
					}
				} else {
					if _, ok := waiting[t.Name()]; !ok {
						if _, complete := localHashCompletionCheck(d.ctx, t, fileInfo, downloadComplete, &d.stats, d.lock); complete {
							d.logger.Trace("[snapshots] Ignoring download request - already complete", "file", t.Name(), "hash", t.InfoHash())
							continue
						}

						waiting[t.Name()] = struct{}{}
					}
				}

				switch {
				case len(t.PeerConns()) > 0:
					d.logger.Debug("[snapshots] Downloading from BitTorrent", "file", t.Name(), "peers", len(t.PeerConns()), "webpeers", len(t.WebseedPeerConns()))
					delete(waiting, t.Name())
					d.torrentDownload(t, downloadComplete, sem)
				case len(t.WebseedPeerConns()) > 0:
					if d.webDownloadClient != nil {
						var peerUrls []*url.URL

						for _, peer := range t.WebseedPeerConns() {
							if peerUrl, err := webPeerUrl(peer); err == nil {
								peerUrls = append(peerUrls, peerUrl)
							}
						}

						d.logger.Debug("[snapshots] Downloading from webseed", "file", t.Name(), "webpeers", len(t.WebseedPeerConns()))
						delete(waiting, t.Name())
						session, err := d.webDownload(peerUrls, t, nil, downloadComplete, sem)

						if err != nil {
							d.logger.Warn("Can't complete web download", "file", t.Info().Name, "err", err)

							if session == nil {
								delete(waiting, t.Name())
								d.torrentDownload(t, downloadComplete, sem)
							}

							continue
						}
					} else {
						d.logger.Debug("[snapshots] Downloading from torrent", "file", t.Name(), "peers", len(t.PeerConns()), "webpeers", len(t.WebseedPeerConns()))
						delete(waiting, t.Name())
						d.torrentDownload(t, downloadComplete, sem)
					}
				default:
					if d.webDownloadClient != nil {
						d.lock.RLock()
						webDownload, ok := d.webDownloadInfo[t.Name()]
						d.lock.RUnlock()

						if !ok {
							var mismatches []*seedHash
							var err error

							webDownload, mismatches, err = d.getWebDownloadInfo(t)

							if err != nil {
								if len(mismatches) > 0 {
									seedHashMismatches[t.InfoHash()] = append(seedHashMismatches[t.InfoHash()], mismatches...)
									logSeedHashMismatches(t.InfoHash(), t.Name(), seedHashMismatches, d.logger)
								}

								d.logger.Warn("Can't complete web download", "file", t.Info().Name, "err", err)
								continue
							}
						}

						root, _ := path.Split(webDownload.url.String())
						peerUrl, err := url.Parse(root)

						if err != nil {
							d.logger.Warn("Can't complete web download", "file", t.Info().Name, "err", err)
							continue
						}

						d.lock.Lock()
						delete(d.webDownloadInfo, t.Name())
						d.lock.Unlock()

						d.logger.Debug("[snapshots] Downloading from web", "file", t.Name(), "webpeers", len(t.WebseedPeerConns()))
						delete(waiting, t.Name())
						d.webDownload([]*url.URL{peerUrl}, t, &webDownload, downloadComplete, sem)
						continue
					}

					d.logger.Debug("[snapshots] Downloading from torrent", "file", t.Name(), "peers", len(t.PeerConns()))
					delete(waiting, t.Name())
					d.torrentDownload(t, downloadComplete, sem)
				}
			}

			d.lock.Lock()
			lastMetadatUpdate := d.stats.LastMetadataUpdate
			d.lock.Unlock()

			if lastMetadatUpdate != nil &&
				((len(available) == 0 && time.Since(*lastMetadatUpdate) > 30*time.Second) ||
					time.Since(*lastMetadatUpdate) > 5*time.Minute) {

				for _, t := range d.torrentClient.Torrents() {
					if t.Info() == nil {
						if isComplete, _, _ := d.checkComplete(t.Name()); isComplete {
							continue
						}

						d.lock.RLock()
						_, ok := d.webDownloadInfo[t.Name()]
						d.lock.RUnlock()

						if !ok {
							if _, ok := seedHashMismatches[t.InfoHash()]; ok {
								continue
							}

							info, mismatches, err := d.getWebDownloadInfo(t)

							seedHashMismatches[t.InfoHash()] = append(seedHashMismatches[t.InfoHash()], mismatches...)

							if err != nil {
								if len(mismatches) > 0 {
									logSeedHashMismatches(t.InfoHash(), t.Name(), seedHashMismatches, d.logger)
								}
								continue
							}

							d.lock.Lock()
							d.webDownloadInfo[t.Name()] = info
							d.lock.Unlock()
						}
					} else {
						d.lock.Lock()
						delete(d.webDownloadInfo, t.Name())
						d.lock.Unlock()
					}
				}
			}
		}
	}()

	logEvery := time.NewTicker(20 * time.Second)
	defer logEvery.Stop()

	statInterval := 20 * time.Second
	statEvery := time.NewTicker(statInterval)
	defer statEvery.Stop()

	var m runtime.MemStats
	for {
		select {
		case <-d.ctx.Done():
			return d.ctx.Err()
		case <-statEvery.C:
			d.ReCalcStats(statInterval)

		case <-logEvery.C:
			if silent {
				continue
			}

			stats := d.Stats()

			dbg.ReadMemStats(&m)
			if stats.Completed {
				d.logger.Info("[snapshots] Seeding",
					"up", common.ByteCount(stats.UploadRate)+"/s",
					"peers", stats.PeersUnique,
					"conns", stats.ConnectionsTotal,
					"files", stats.FilesTotal,
					"alloc", common.ByteCount(m.Alloc), "sys", common.ByteCount(m.Sys),
				)
				continue
			}

			d.logger.Info("[snapshots] Downloading",
				"progress", fmt.Sprintf("%.2f%% %s/%s", stats.Progress, common.ByteCount(stats.BytesCompleted), common.ByteCount(stats.BytesTotal)),
				"downloading", stats.Downloading,
				"download", common.ByteCount(stats.DownloadRate)+"/s",
				"upload", common.ByteCount(stats.UploadRate)+"/s",
				"peers", stats.PeersUnique,
				"conns", stats.ConnectionsTotal,
				"files", stats.FilesTotal,
				"alloc", common.ByteCount(m.Alloc), "sys", common.ByteCount(m.Sys),
			)

			if stats.PeersUnique == 0 {
				ips := d.TorrentClient().BadPeerIPs()
				if len(ips) > 0 {
					d.logger.Info("[snapshots] Stats", "banned", ips)
				}
			}
		}
	}
}
