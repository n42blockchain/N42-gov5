package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/spaolacci/murmur3"
	"golang.org/x/sync/errgroup"
)

// RCloneSession manages file synchronization between local and remote filesystems via rclone.
type RCloneSession struct {
	*RCloneClient
	sync.Mutex
	files           map[string]*rcloneInfo
	oplock          sync.Mutex
	remoteFs        string
	localFs         string
	syncQueue       chan syncRequest
	syncScheduled   atomic.Bool
	activeSyncCount atomic.Int32
	cancel          context.CancelFunc
	headers         http.Header
}

type syncRequest struct {
	ctx       context.Context
	info      map[string]*rcloneInfo
	cerr      chan error
	requests  []*rcloneRequest
	retryTime time.Duration
}

func (c *RCloneClient) NewSession(ctx context.Context, localFs string, remoteFs string, headers http.Header) (*RCloneSession, error) {
	ctx, cancel := context.WithCancel(ctx)

	session := &RCloneSession{
		RCloneClient: c,
		files:        map[string]*rcloneInfo{},
		remoteFs:     remoteFs,
		localFs:      localFs,
		cancel:       cancel,
		syncQueue:    make(chan syncRequest, 100),
		headers:      headers,
	}

	go func() {
		if !strings.HasPrefix(remoteFs, "http") {
			if _, err := session.ReadRemoteDir(ctx, true); err != nil {
				return
			}
		}
		session.syncFiles(ctx)
	}()

	return session, nil
}

func (c *RCloneSession) RemoteFsRoot() string { return c.remoteFs }
func (c *RCloneSession) LocalFsRoot() string  { return c.localFs }
func (c *RCloneSession) Stop()                { c.cancel() }

func (c *RCloneSession) Label() string {
	return strconv.FormatUint(murmur3.Sum64([]byte(c.localFs+"<->"+c.remoteFs)), 36)
}

func (c *RCloneSession) Upload(ctx context.Context, files ...string) error {
	c.Lock()

	reqInfo := map[string]*rcloneInfo{}
	for _, file := range files {
		info, ok := c.files[file]

		if !ok || info.localInfo == nil {
			localInfo, err := os.Stat(filepath.Join(c.localFs, file))
			if err != nil {
				c.Unlock()
				return fmt.Errorf("can't upload: %s: %w", file, err)
			}
			if !localInfo.Mode().IsRegular() || localInfo.Size() == 0 {
				c.Unlock()
				return fmt.Errorf("can't upload: %s: file is not uploadable", file)
			}

			if ok {
				info.localInfo = localInfo
			} else {
				newInfo := &rcloneInfo{
					file:      file,
					localInfo: localInfo,
				}
				if snapInfo, isStateFile, ok := snaptype.ParseFileName(c.localFs, file); ok && !isStateFile {
					newInfo.snapInfo = &snapInfo
				}
				c.files[file] = newInfo
			}
		} else {
			reqInfo[file] = info
		}
	}
	c.Unlock()

	cerr := make(chan error, 1)
	c.syncQueue <- syncRequest{ctx, reqInfo, cerr,
		[]*rcloneRequest{{
			Group: c.Label(),
			SrcFs: c.localFs,
			DstFs: c.remoteFs,
			Filter: rcloneFilter{
				IncludeRule: files,
			}}}, 0}

	return <-cerr
}

func (c *RCloneSession) Download(ctx context.Context, files ...string) error {
	reqInfo := map[string]*rcloneInfo{}
	var fileRequests []*rcloneRequest

	if strings.HasPrefix(c.remoteFs, "http") {
		var headers string
		var comma string
		for header, values := range c.headers {
			for _, value := range values {
				headers += fmt.Sprintf("%s%s=%s", comma, header, value)
				comma = ","
			}
		}

		for _, file := range files {
			reqInfo[file] = &rcloneInfo{file: file}
			fileRequests = append(fileRequests, &rcloneRequest{
				Group: c.remoteFs,
				SrcFs: rcloneFs{
					Type:    "http",
					Url:     c.remoteFs,
					Headers: headers,
				},
				SrcRemote: file,
				DstFs:     c.localFs,
				DstRemote: file,
			})
		}
	} else {
		c.Lock()
		if len(c.files) == 0 {
			c.Unlock()
			if _, err := c.ReadRemoteDir(ctx, false); err != nil {
				return fmt.Errorf("can't download: %s: %w", files, err)
			}
			c.Lock()
		}

		for _, file := range files {
			info, ok := c.files[file]
			if !ok || info.remoteInfo.Size == 0 {
				c.Unlock()
				return fmt.Errorf("can't download: %s: %w", file, os.ErrNotExist)
			}
			reqInfo[file] = info
		}
		c.Unlock()

		fileRequests = append(fileRequests, &rcloneRequest{
			Group: c.Label(),
			SrcFs: c.remoteFs,
			DstFs: c.localFs,
			Filter: rcloneFilter{
				IncludeRule: files,
			}})
	}

	cerr := make(chan error, 1)
	c.syncQueue <- syncRequest{ctx, reqInfo, cerr, fileRequests, 0}
	return <-cerr
}

func (c *RCloneSession) Cat(ctx context.Context, file string) (io.Reader, error) {
	rclone, err := exec.LookPath("rclone")
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, rclone, "cat", c.remoteFs+"/"+file)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return stdout, nil
}

func (c *RCloneSession) ReadLocalDir(ctx context.Context) ([]fs.DirEntry, error) {
	return dir.ReadDir(c.localFs)
}

func (c *RCloneSession) ReadRemoteDir(ctx context.Context, refresh bool) ([]fs.DirEntry, error) {
	if len(c.remoteFs) == 0 {
		return nil, fmt.Errorf("remote fs undefined")
	}

	c.oplock.Lock()
	defer c.oplock.Unlock()

	c.Lock()
	fileCount := len(c.files)
	c.Unlock()

	if fileCount == 0 || refresh {
		if err := c.fetchRemoteFileList(ctx); err != nil {
			return nil, err
		}
	}

	entries := make([]fs.DirEntry, 0, len(c.files))
	for _, info := range c.files {
		if info.remoteInfo.Size > 0 {
			entries = append(entries, &dirEntry{&fileInfo{info}})
		}
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

func (c *RCloneSession) fetchRemoteFileList(ctx context.Context) error {
	listBody, err := json.Marshal(struct {
		Fs     string `json:"fs"`
		Remote string `json:"remote"`
	}{Fs: c.remoteFs, Remote: ""})
	if err != nil {
		return fmt.Errorf("can't marshal list request: %w", err)
	}

	listRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.rcloneUrl+"/operations/list", bytes.NewBuffer(listBody))
	if err != nil {
		return fmt.Errorf("can't create list request: %w", err)
	}
	listRequest.Header.Set("Content-Type", "application/json")

	var response *http.Response
	for i := 0; i < 10; i++ {
		response, err = c.rcloneSession.Do(listRequest) //nolint:bodyclose
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("can't get remote list: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		e := struct {
			Error string `json:"error"`
		}{}
		if err := json.Unmarshal(body, &e); err == nil && strings.Contains(e.Error, "AccessDenied") {
			return fmt.Errorf("can't get remote list: %w", ErrAccessDenied)
		}
		return fmt.Errorf("can't get remote list: %s: %s", response.Status, string(body))
	}

	responseBody := struct {
		List []remoteInfo `json:"list"`
	}{}
	if err := json.NewDecoder(response.Body).Decode(&responseBody); err != nil {
		return fmt.Errorf("can't decode remote list: %w", err)
	}

	for _, fi := range responseBody.List {
		localInfo, _ := os.Stat(filepath.Join(c.localFs, fi.Name))
		c.Lock()
		c.upsertFileInfo(fi, localInfo)
		c.Unlock()
	}
	return nil
}

// upsertFileInfo updates or inserts file info from a remote listing. Must be called with c.Mutex held.
func (c *RCloneSession) upsertFileInfo(fi remoteInfo, localInfo os.FileInfo) {
	if rcinfo, ok := c.files[fi.Name]; ok {
		rcinfo.localInfo = localInfo
		rcinfo.remoteInfo = fi
		if snapInfo, isStateFile, ok := snaptype.ParseFileName(c.localFs, fi.Name); ok && !isStateFile {
			rcinfo.snapInfo = &snapInfo
		} else {
			rcinfo.snapInfo = nil
		}
	} else {
		info := &rcloneInfo{
			file:       fi.Name,
			localInfo:  localInfo,
			remoteInfo: fi,
		}
		if snapInfo, isStateFile, ok := snaptype.ParseFileName(c.localFs, fi.Name); ok && !isStateFile {
			info.snapInfo = &snapInfo
		}
		c.files[fi.Name] = info
	}
}

func (c *RCloneSession) syncFiles(ctx context.Context) {
	if !c.syncScheduled.CompareAndSwap(false, true) {
		return
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(16)

	const minRetryTime = 30 * time.Second
	const maxRetryTime = 300 * time.Second

	retryFn := func(request syncRequest) {
		switch {
		case request.retryTime == 0:
			request.retryTime = minRetryTime
		case request.retryTime < maxRetryTime:
			request.retryTime += request.retryTime
		default:
			request.retryTime = maxRetryTime
		}

		retryTimer := time.NewTicker(request.retryTime)
		select {
		case <-request.ctx.Done():
			request.cerr <- request.ctx.Err()
			return
		case <-retryTimer.C:
		}

		c.Lock()
		syncQueue := c.syncQueue
		c.Unlock()

		if syncQueue != nil {
			syncQueue <- request
		} else {
			request.cerr <- fmt.Errorf("no sync queue available")
		}
	}

	go func() {
		logEvery := time.NewTicker(20 * time.Second)
		defer logEvery.Stop()

		select {
		case <-gctx.Done():
			if syncCount := int(c.activeSyncCount.Load()) + len(c.syncQueue); syncCount > 0 {
				log.Debug("[rclone] Synced files", "processed", fmt.Sprintf("%d/%d", c.activeSyncCount.Load(), syncCount))
			}
			c.Lock()
			syncQueue := c.syncQueue
			c.syncQueue = nil
			c.Unlock()
			if syncQueue != nil {
				close(syncQueue)
			}
			return
		case <-logEvery.C:
			if syncCount := int(c.activeSyncCount.Load()) + len(c.syncQueue); syncCount > 0 {
				log.Debug("[rclone] Syncing files", "progress", fmt.Sprintf("%d/%d", c.activeSyncCount.Load(), syncCount))
			}
		}
	}()

	go func() {
		for req := range c.syncQueue {
			if gctx.Err() != nil {
				req.cerr <- gctx.Err()
				continue
			}

			func(req syncRequest) {
				g.Go(func() error {
					c.activeSyncCount.Add(1)
					defer func() {
						c.activeSyncCount.Add(-1)
						if r := recover(); r != nil {
							log.Error("[rclone] snapshot sync failed", "err", r, "stack", dbg.Stack())
							if gctx.Err() != nil {
								req.cerr <- gctx.Err()
							} else if err, ok := r.(error); ok {
								req.cerr <- fmt.Errorf("snapshot sync failed: %w", err)
							} else {
								req.cerr <- fmt.Errorf("snapshot sync failed: %s", r)
							}
						}
					}()

					if req.ctx.Err() != nil {
						req.cerr <- req.ctx.Err()
						return nil //nolint:nilerr
					}

					for _, fileReq := range req.requests {
						var syncErr error
						if _, ok := fileReq.SrcFs.(rcloneFs); ok {
							syncErr = c.copyFile(gctx, fileReq)
						} else {
							syncErr = c.sync(gctx, fileReq)
						}
						if syncErr != nil {
							if gctx.Err() != nil {
								req.cerr <- gctx.Err()
							} else {
								go retryFn(req)
							}
							return nil //nolint:nilerr
						}
					}

					for _, info := range req.info {
						localInfo, _ := os.Stat(filepath.Join(c.localFs, info.file))
						info.Lock()
						info.localInfo = localInfo
						info.remoteInfo = remoteInfo{
							Name:    info.file,
							Size:    uint64(localInfo.Size()),
							ModTime: localInfo.ModTime(),
						}
						info.Unlock()
					}

					req.cerr <- nil
					return nil
				})
			}(req)
		}

		c.syncScheduled.Store(false)
		if err := g.Wait(); err != nil {
			c.logger.Debug("[rclone] sync failed", "err", err)
		}
	}()
}
