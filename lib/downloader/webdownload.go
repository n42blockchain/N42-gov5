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

	// todo this function does not exit on first matched webseed hash, could make unexpected results
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

func selectDownloadPeer(ctx context.Context, peerUrls []*url.URL, t *torrent.Torrent) (string, error) {
	switch len(peerUrls) {
	case 0:
		return "", fmt.Errorf("no download peers")

	case 1:
		downloadUrl := peerUrls[0].JoinPath(t.Name())
		peerInfo, err := getWebpeerTorrentInfo(ctx, downloadUrl)

		if err == nil && bytes.Equal(peerInfo.HashInfoBytes().Bytes(), t.InfoHash().Bytes()) {
			return peerUrls[0].String(), nil
		}

	default:
		peerIndex := rand.Intn(len(peerUrls))
		peerUrl := peerUrls[peerIndex]
		downloadUrl := peerUrl.JoinPath(t.Name())
		peerInfo, err := getWebpeerTorrentInfo(ctx, downloadUrl)

		if err == nil && bytes.Equal(peerInfo.HashInfoBytes().Bytes(), t.InfoHash().Bytes()) {
			return peerUrl.String(), nil
		}

		for i := range peerUrls {
			if i == peerIndex {
				continue
			}
			peerInfo, err := getWebpeerTorrentInfo(ctx, downloadUrl)

			if err == nil && bytes.Equal(peerInfo.HashInfoBytes().Bytes(), t.InfoHash().Bytes()) {
				return peerUrl.String(), nil
			}
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
		var err error
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

			func() {
				d.lock.Lock()
				defer d.lock.Unlock()

				torrentLimit := d.cfg.ClientConfig.DownloadRateLimiter.Limit()
				rcloneLimit := d.webDownloadClient.GetBwLimit()

				d.cfg.ClientConfig.DownloadRateLimiter.SetLimit(torrentLimit - rate.Limit(limit))
				d.webDownloadClient.SetBwLimit(d.ctx, rcloneLimit+rate.Limit(limit))
			}()

			defer func() {
				d.lock.Lock()
				defer d.lock.Unlock()

				torrentLimit := d.cfg.ClientConfig.DownloadRateLimiter.Limit()
				rcloneLimit := d.webDownloadClient.GetBwLimit()

				d.cfg.ClientConfig.DownloadRateLimiter.SetLimit(torrentLimit + rate.Limit(limit))
				d.webDownloadClient.SetBwLimit(d.ctx, rcloneLimit-rate.Limit(limit))
			}()
		}

		err := session.Download(d.ctx, name)

		if err != nil {
			d.logger.Error("Web download failed", "file", name, "err", err)
		}

		localHash, err := fileHashBytes(d.ctx, info, &d.stats, d.lock)

		if err == nil {
			if !bytes.Equal(infoHash.Bytes(), localHash) {
				err = fmt.Errorf("hash mismatch: expected: 0x%x, got: 0x%x", infoHash.Bytes(), localHash)

				d.logger.Error("Web download failed", "file", name, "url", peerUrl, "err", err)

				if ferr := os.Remove(info.Path); ferr != nil {
					d.logger.Warn("Couldn't remove invalid file", "file", name, "path", info.Path, "err", ferr)
				}
			}
		} else {
			d.logger.Error("Web download failed", "file", name, "url", peerUrl, "err", err)
		}

		statusChan <- downloadStatus{
			name:     name,
			length:   length,
			infoHash: infoHash,
			spec:     spec,
			err:      err,
		}
	}()

	return session, nil
}

func webPeerUrl(peer *torrent.Peer) (*url.URL, error) {
	root, _ := path.Split(strings.Trim(strings.TrimPrefix(peer.String(), "webseed peer for "), "\""))
	return url.Parse(root)
}
