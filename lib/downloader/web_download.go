package downloader

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types/infohash"
	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"

	"github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/downloader/downloadercfg"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
)

type webDownloadInfo struct {
	url     *url.URL
	length  int64
	md5     string
	torrent *torrent.Torrent
}

type downloadStatus struct {
	name     string
	length   int64
	infoHash infohash.T
	spec     *torrent.TorrentSpec
	err      error
}

type seedHash struct {
	url      *url.URL
	hash     *infohash.T
	reported bool
}

func (d *Downloader) getWebDownloadInfo(t *torrent.Torrent) (webDownloadInfo, []*seedHash, error) {
	d.lock.RLock()
	info, ok := d.webDownloadInfo[t.Name()]
	d.lock.RUnlock()
	if ok {
		return info, nil, nil
	}

	infos, seedHashMismatches, err := d.webseeds.getWebDownloadInfo(d.ctx, t)
	if err != nil || len(infos) == 0 {
		return webDownloadInfo{}, seedHashMismatches, fmt.Errorf("can't find download info: %w", err)
	}
	return infos[0], seedHashMismatches, nil
}

func getWebpeerTorrentInfo(ctx context.Context, downloadUrl *url.URL) (*metainfo.MetaInfo, error) {
	torrentRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadUrl.String()+".torrent", nil)
	if err != nil {
		return nil, err
	}

	torrentResponse, err := http.DefaultClient.Do(torrentRequest)
	if err != nil {
		return nil, err
	}
	defer torrentResponse.Body.Close()

	if torrentResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("can't get webpeer torrent unexpected http response: %s", torrentResponse.Status)
	}
	return metainfo.Load(torrentResponse.Body)
}

// matchesPeerHash checks if a peer URL serves a torrent with the expected info hash.
func matchesPeerHash(ctx context.Context, peerUrl *url.URL, t *torrent.Torrent) bool {
	downloadUrl := peerUrl.JoinPath(t.Name())
	peerInfo, err := getWebpeerTorrentInfo(ctx, downloadUrl)
	if err != nil {
		return false
	}
	return bytes.Equal(peerInfo.HashInfoBytes().Bytes(), t.InfoHash().Bytes())
}

func selectDownloadPeer(ctx context.Context, peerUrls []*url.URL, t *torrent.Torrent) (string, error) {
	if len(peerUrls) == 0 {
		return "", fmt.Errorf("no download peers")
	}

	// Try a random peer first for load distribution
	startIdx := rand.Intn(len(peerUrls))
	for i := 0; i < len(peerUrls); i++ {
		idx := (startIdx + i) % len(peerUrls)
		if matchesPeerHash(ctx, peerUrls[idx], t) {
			return peerUrls[idx].String(), nil
		}
	}
	return "", fmt.Errorf("can't find download peer")
}

func (d *Downloader) webDownload(peerUrls []*url.URL, t *torrent.Torrent, i *webDownloadInfo, statusChan chan downloadStatus, sem *semaphore.Weighted) (*RCloneSession, error) {
	if d.webDownloadClient == nil {
		return nil, fmt.Errorf("webdownload client not enabled")
	}

	peerUrl, err := selectDownloadPeer(d.ctx, peerUrls, t)
	if err != nil {
		return nil, err
	}
	peerUrl = strings.TrimSuffix(peerUrl, "/")

	session, ok := d.webDownloadSessions[peerUrl]
	if !ok {
		session, err = d.webDownloadClient.NewSession(d.ctx, d.SnapDir(), peerUrl, cloudflareHeaders)
		if err != nil {
			return nil, err
		}
		d.webDownloadSessions[peerUrl] = session
	}

	name := t.Name()
	mi := t.Metainfo()
	infoHash := t.InfoHash()

	var length int64
	if i != nil {
		length = i.length
	} else {
		length = t.Length()
	}

	magnet := mi.Magnet(&infoHash, &metainfo.Info{Name: name})
	spec, err := torrent.TorrentSpecFromMagnetUri(magnet.String())
	if err != nil {
		return session, fmt.Errorf("can't get torrent spec for %s from info: %w", t.Info().Name, err)
	}
	spec.ChunkSize = downloadercfg.DefaultNetworkChunkSize
	spec.DisallowDataDownload = true

	info, _, ok := snaptype.ParseFileName(d.SnapDir(), name)
	if !ok {
		return nil, fmt.Errorf("can't parse filename: %s", name)
	}

	d.lock.Lock()
	t.Drop()
	d.downloading[name] = struct{}{}
	d.lock.Unlock()

	d.wg.Add(1)
	if err := sem.Acquire(d.ctx, 1); err != nil {
		d.logger.Warn("Failed to acquire download semaphore", "err", err)
		return nil, err
	}

	go func() {
		defer d.wg.Done()
		defer sem.Release(1)

		if dir.FileExist(info.Path) {
			if err := os.Remove(info.Path); err != nil {
				d.logger.Warn("Couldn't remove previous file before download", "file", name, "path", info.Path, "err", err)
			}
		}

		if d.downloadLimit != nil {
			limit := float64(*d.downloadLimit) / float64(d.cfg.DownloadSlots)
			d.adjustBwLimits(limit)
			defer d.adjustBwLimits(-limit)
		}

		dlErr := session.Download(d.ctx, name)
		if dlErr != nil {
			d.logger.Error("Web download failed", "file", name, "err", dlErr)
		}

		localHash, hashErr := fileHashBytes(d.ctx, info, &d.stats, d.lock)
		if hashErr != nil {
			d.logger.Error("Web download failed", "file", name, "url", peerUrl, "err", hashErr)
			statusChan <- downloadStatus{name: name, length: length, infoHash: infoHash, spec: spec, err: hashErr}
			return
		}

		if !bytes.Equal(infoHash.Bytes(), localHash) {
			mismatchErr := fmt.Errorf("hash mismatch: expected: 0x%x, got: 0x%x", infoHash.Bytes(), localHash)
			d.logger.Error("Web download failed", "file", name, "url", peerUrl, "err", mismatchErr)
			if ferr := os.Remove(info.Path); ferr != nil {
				d.logger.Warn("Couldn't remove invalid file", "file", name, "path", info.Path, "err", ferr)
			}
			statusChan <- downloadStatus{name: name, length: length, infoHash: infoHash, spec: spec, err: mismatchErr}
			return
		}

		statusChan <- downloadStatus{name: name, length: length, infoHash: infoHash, spec: spec}
	}()

	return session, nil
}

// adjustBwLimits shifts bandwidth between the torrent client and the rclone web download client.
func (d *Downloader) adjustBwLimits(delta float64) {
	d.lock.Lock()
	defer d.lock.Unlock()

	torrentLimit := d.cfg.ClientConfig.DownloadRateLimiter.Limit()
	rcloneLimit := d.webDownloadClient.GetBwLimit()
	d.cfg.ClientConfig.DownloadRateLimiter.SetLimit(torrentLimit - rate.Limit(delta))
	d.webDownloadClient.SetBwLimit(d.ctx, rcloneLimit+rate.Limit(delta))
}

func webPeerUrl(peer *torrent.Peer) (*url.URL, error) {
	root, _ := path.Split(strings.Trim(strings.TrimPrefix(peer.String(), "webseed peer for "), "\""))
	return url.Parse(root)
}
