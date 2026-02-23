package downloader

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/diagnostics"
)

type downloadProgress struct {
	time     time.Time
	progress float32
}

type AggStats struct {
	MetadataReady, FilesTotal int32
	LastMetadataUpdate        *time.Time
	PeersUnique               int32
	ConnectionsTotal          uint64
	Downloading               int32

	Completed bool
	Progress  float32

	BytesCompleted, BytesTotal     uint64
	DroppedCompleted, DroppedTotal uint64

	BytesDownload, BytesUpload uint64
	UploadRate, DownloadRate   uint64
	LocalFileHashes            int
	LocalFileHashTime          time.Duration

	WebseedTripCount     *atomic.Int64
	WebseedDiscardCount  *atomic.Int64
	WebseedServerFails   *atomic.Int64
	WebseedBytesDownload *atomic.Int64

	lastTorrentStatus time.Time
	downloadProgress  map[string]downloadProgress
}

func (d *Downloader) ReCalcStats(interval time.Duration) {
	d.lock.Lock()
	defer d.lock.Unlock()
	//Call this methods outside of `lock` critical section, because they have own locks with contention
	torrents := d.torrentClient.Torrents()
	connStats := d.torrentClient.ConnStats()
	peers := make(map[torrent.PeerID]struct{}, 16)

	prevStats, stats := d.stats, d.stats

	stats.Completed = true
	stats.BytesDownload = uint64(connStats.BytesReadUsefulIntendedData.Int64())
	stats.BytesUpload = uint64(connStats.BytesWrittenData.Int64())

	lastMetadataReady := stats.MetadataReady

	stats.BytesTotal, stats.BytesCompleted, stats.ConnectionsTotal, stats.MetadataReady =
		atomic.LoadUint64(&stats.DroppedTotal), atomic.LoadUint64(&stats.DroppedCompleted), 0, 0

	var zeroProgress []string
	var noMetadata []string

	isDiagEnabled := diagnostics.TypeOf(diagnostics.SnapshoFilesList{}).Enabled()
	if isDiagEnabled {
		filesList := make([]string, 0, len(torrents))
		for _, t := range torrents {
			filesList = append(filesList, t.Name())
		}
		diagnostics.Send(diagnostics.SnapshoFilesList{Files: filesList})
	}

	downloading := map[string]float32{}

	for file := range d.downloading {
		downloading[file] = 0
	}

	var dbInfo int
	var tComplete int
	var torrentInfo int

	for _, t := range torrents {
		select {
		case <-t.GotInfo():
		default: // if some torrents have no metadata, we are for-sure uncomplete
			stats.Completed = false
			noMetadata = append(noMetadata, t.Name())
			continue
		}

		torrentName := t.Name()
		torrentComplete := t.Complete.Bool()
		torrentInfo++
		stats.MetadataReady++

		// call methods once - to reduce internal mutex contention
		peersOfThisFile := t.PeerConns()
		weebseedPeersOfThisFile := t.WebseedPeerConns()

		tLen := t.Length()

		var bytesCompleted int64

		if torrentComplete {
			tComplete++
			bytesCompleted = t.Length()
			delete(downloading, torrentName)
		} else {
			bytesCompleted = t.BytesCompleted()
		}
		progress := float32(float64(100) * (float64(bytesCompleted) / float64(tLen)))

		if _, ok := downloading[torrentName]; ok {

			if progress != stats.downloadProgress[torrentName].progress {
				stats.downloadProgress[torrentName] = downloadProgress{time: time.Now(), progress: progress}
			}
		} else {
			// we only care about progress of downloading files
			delete(stats.downloadProgress, torrentName)
		}

		stats.BytesCompleted += uint64(bytesCompleted)
		stats.BytesTotal += uint64(tLen)

		for _, peer := range peersOfThisFile {
			stats.ConnectionsTotal++
			peers[peer.PeerID] = struct{}{}
		}

		webseedRates, webseeds := getWebseedsRatesForlogs(weebseedPeersOfThisFile, torrentName, t.Complete.Bool())
		rates, peers := getPeersRatesForlogs(peersOfThisFile, torrentName)

		if !torrentComplete {
			if info, err := d.torrentInfo(torrentName); err == nil {
				if info != nil {
					dbInfo++
				}
			} else if _, ok := d.webDownloadInfo[torrentName]; ok {
				stats.MetadataReady++
			} else {
				noMetadata = append(noMetadata, torrentName)
			}

			if progress == 0 {
				zeroProgress = append(zeroProgress, torrentName)
			}
		}

		// more detailed statistic: download rate of each peer (for each file)
		if !torrentComplete && progress != 0 {
			if _, ok := downloading[torrentName]; ok {
				downloading[torrentName] = progress
			}

			d.logger.Log(d.verbosity, "[snapshots] progress", "file", torrentName, "progress", fmt.Sprintf("%.2f%%", progress), "peers", len(peersOfThisFile), "webseeds", len(weebseedPeersOfThisFile))
			d.logger.Log(d.verbosity, "[snapshots] webseed peers", webseedRates...)
			d.logger.Log(d.verbosity, "[snapshots] bittorrent peers", rates...)
		}

		diagnostics.Send(diagnostics.SegmentDownloadStatistics{
			Name:            torrentName,
			TotalBytes:      uint64(tLen),
			DownloadedBytes: uint64(bytesCompleted),
			Webseeds:        webseeds,
			Peers:           peers,
		})

		stats.Completed = stats.Completed && torrentComplete
	}

	var webTransfers int32

	if d.webDownloadClient != nil {
		webStats, _ := d.webDownloadClient.Stats(d.ctx)

		if webStats != nil {
			if len(webStats.Transferring) != 0 && stats.Completed {
				stats.Completed = false
			}

			for _, transfer := range webStats.Transferring {
				stats.MetadataReady++
				webTransfers++

				bytesCompleted := transfer.Bytes
				tLen := transfer.Size
				transferName := transfer.Name

				delete(downloading, transferName)

				if bytesCompleted > tLen {
					bytesCompleted = tLen
				}

				stats.BytesCompleted += bytesCompleted
				stats.BytesTotal += tLen

				stats.BytesDownload += bytesCompleted

				if transfer.Percentage == 0 {
					zeroProgress = append(zeroProgress, transferName)
				}

				var seeds []diagnostics.SegmentPeer
				var webseedRates []interface{}
				if peerUrl, err := url.Parse(transfer.Group); err == nil {
					rate := uint64(transfer.SpeedAvg)
					seeds = []diagnostics.SegmentPeer{
						{
							Url:          peerUrl.Host,
							DownloadRate: rate,
						}}

					if shortUrl, err := url.JoinPath(peerUrl.Host, peerUrl.Path); err == nil {
						webseedRates = []interface{}{strings.TrimSuffix(shortUrl, "/"), fmt.Sprintf("%s/s", common.ByteCount(rate))}
					}
				}

				// more detailed statistic: download rate of each peer (for each file)
				if transfer.Percentage != 0 {
					d.logger.Log(d.verbosity, "[snapshots] progress", "file", transferName, "progress", fmt.Sprintf("%.2f%%", float32(transfer.Percentage)), "webseeds", 1)
					d.logger.Log(d.verbosity, "[snapshots] web peers", webseedRates...)
				}

				diagnostics.Send(diagnostics.SegmentDownloadStatistics{
					Name:            transferName,
					TotalBytes:      tLen,
					DownloadedBytes: bytesCompleted,
					Webseeds:        seeds,
				})
			}
		}
	}

	if len(downloading) > 0 {
		if d.webDownloadClient != nil {
			webTransfers += int32(len(downloading))
		}

		stats.Completed = false
	}

	if !stats.Completed {
		d.logger.Debug("[snapshots] info",
			"len", len(torrents),
			"webTransfers", webTransfers,
			"torrent", torrentInfo,
			"db", dbInfo,
			"t-complete", tComplete,
			"webseed-trips", stats.WebseedTripCount.Load(),
			"webseed-discards", stats.WebseedDiscardCount.Load(),
			"webseed-fails", stats.WebseedServerFails.Load(),
			"webseed-bytes", common.ByteCount(uint64(stats.WebseedBytesDownload.Load())),
			"localHashes", stats.LocalFileHashes, "localHashTime", stats.LocalFileHashTime)
	}

	if lastMetadataReady != stats.MetadataReady {
		now := time.Now()
		stats.LastMetadataUpdate = &now
	}

	if len(noMetadata) > 0 {
		amount := len(noMetadata)
		if len(noMetadata) > 5 {
			noMetadata = append(noMetadata[:5], "...")
		}
		d.logger.Info("[snapshots] no metadata yet", "files", amount, "list", strings.Join(noMetadata, ","))
	}

	var noDownloadProgress []string

	if len(zeroProgress) > 0 {
		amount := len(zeroProgress)

		for _, file := range zeroProgress {
			if _, ok := downloading[file]; ok {
				noDownloadProgress = append(noDownloadProgress, file)
			}
		}

		if len(zeroProgress) > 5 {
			zeroProgress = append(zeroProgress[:5], "...")
		}

		d.logger.Info("[snapshots] no progress yet", "files", amount, "list", strings.Join(zeroProgress, ","))
	}

	if len(downloading) > 0 {
		amount := len(downloading)

		files := make([]string, 0, len(downloading))
		for file, progress := range downloading {
			files = append(files, fmt.Sprintf("%s (%.0f%%)", file, progress))

			if dp, ok := stats.downloadProgress[file]; ok {
				if time.Since(dp.time) > 30*time.Minute {
					noDownloadProgress = append(noDownloadProgress, file)
				}
			}
		}
		sort.Strings(files)

		d.logger.Log(d.verbosity, "[snapshots] downloading", "files", amount, "list", strings.Join(files, ", "))
	}

	if time.Since(stats.lastTorrentStatus) > 5*time.Minute {
		stats.lastTorrentStatus = time.Now()

		if len(noDownloadProgress) > 0 {
			progressStatus := getProgressStatus(d.torrentClient, noDownloadProgress)
			for file, status := range progressStatus {
				d.logger.Debug(fmt.Sprintf("[snapshots] torrent status: %s\n    %s", file,
					string(bytes.TrimRight(bytes.ReplaceAll(status, []byte("\n"), []byte("\n    ")), "\n "))))
			}
		}
	}

	if stats.BytesDownload > prevStats.BytesDownload {
		stats.DownloadRate = (stats.BytesDownload - prevStats.BytesDownload) / uint64(interval.Seconds())
	} else {
		stats.DownloadRate = prevStats.DownloadRate / 2
	}

	if stats.BytesUpload > prevStats.BytesUpload {
		stats.UploadRate = (stats.BytesUpload - prevStats.BytesUpload) / uint64(interval.Seconds())
	} else {
		stats.UploadRate = prevStats.UploadRate / 2
	}

	if stats.BytesTotal == 0 {
		stats.Progress = 0
	} else {
		stats.Progress = float32(float64(100) * (float64(stats.BytesCompleted) / float64(stats.BytesTotal)))
		if int(stats.Progress) == 100 && !stats.Completed {
			stats.Progress = 99.9
		}
	}

	stats.PeersUnique = int32(len(peers))
	stats.FilesTotal = int32(len(torrents)) + webTransfers

	d.stats = stats
}

type filterWriter struct {
	files     map[string][]byte
	remainder []byte
	file      string
}

func (f *filterWriter) Write(p []byte) (n int, err error) {
	written := len(p)

	p = append(f.remainder, p...)

	for len(p) > 0 {
		scanned, line, _ := bufio.ScanLines(p, false)

		if scanned > 0 {
			if len(f.file) > 0 {
				if len(line) == 0 {
					f.file = ""
				} else {
					line = append(line, '\n')
					f.files[f.file] = append(f.files[f.file], line...)
				}
			} else {
				if _, ok := f.files[string(line)]; ok {
					f.file = string(line)
				}
			}

			p = p[scanned:]
		} else {
			f.remainder = p
			p = nil
		}
	}
	return written, nil
}

func getProgressStatus(torrentClient *torrent.Client, noDownloadProgress []string) map[string][]byte {
	writer := filterWriter{
		files: map[string][]byte{},
	}

	for _, file := range noDownloadProgress {
		writer.files[file] = nil
	}

	torrentClient.WriteStatus(&writer)

	return writer.files
}

func getWebseedsRatesForlogs(weebseedPeersOfThisFile []*torrent.Peer, fName string, finished bool) ([]interface{}, []diagnostics.SegmentPeer) {
	seeds := make([]diagnostics.SegmentPeer, 0, len(weebseedPeersOfThisFile))
	webseedRates := make([]interface{}, 0, len(weebseedPeersOfThisFile)*2)
	webseedRates = append(webseedRates, "file", fName)
	for _, peer := range weebseedPeersOfThisFile {
		if peerUrl, err := webPeerUrl(peer); err == nil {
			if shortUrl, err := url.JoinPath(peerUrl.Host, peerUrl.Path); err == nil {
				rate := uint64(peer.DownloadRate())
				if !finished {
					seed := diagnostics.SegmentPeer{
						Url:          peerUrl.Host,
						DownloadRate: rate,
					}
					seeds = append(seeds, seed)
				}
				webseedRates = append(webseedRates, strings.TrimSuffix(shortUrl, "/"), fmt.Sprintf("%s/s", common.ByteCount(rate)))
			}
		}
	}

	return webseedRates, seeds
}

func getPeersRatesForlogs(peersOfThisFile []*torrent.PeerConn, fName string) ([]interface{}, []diagnostics.SegmentPeer) {
	peers := make([]diagnostics.SegmentPeer, 0, len(peersOfThisFile))
	rates := make([]interface{}, 0, len(peersOfThisFile)*2)
	rates = append(rates, "file", fName)

	for _, peer := range peersOfThisFile {
		dr := uint64(peer.DownloadRate())
		url := fmt.Sprintf("%v", peer.PeerClientName.Load())

		segPeer := diagnostics.SegmentPeer{
			Url:          url,
			DownloadRate: dr,
		}
		peers = append(peers, segPeer)
		rates = append(rates, peer.PeerClientName.Load(), fmt.Sprintf("%s/s", common.ByteCount(dr)))
	}

	return rates, peers
}
