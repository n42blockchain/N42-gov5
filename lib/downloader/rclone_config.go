package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/downloader/snaptype"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// rcloneInfo holds metadata about a file managed by rclone.
type rcloneInfo struct {
	sync.Mutex
	file       string
	snapInfo   *snaptype.FileInfo
	remoteInfo remoteInfo
	localInfo  fs.FileInfo
}

func (i *rcloneInfo) Version() snaptype.Version {
	if i.snapInfo != nil {
		return i.snapInfo.Version
	}
	return snaptype.Version{}
}

func (i *rcloneInfo) From() uint64 {
	if i.snapInfo != nil {
		return i.snapInfo.From
	}
	return 0
}

func (i *rcloneInfo) To() uint64 {
	if i.snapInfo != nil {
		return i.snapInfo.To
	}
	return 0
}

func (i *rcloneInfo) Type() snaptype.Type {
	if i.snapInfo != nil {
		return i.snapInfo.Type
	}
	return nil
}

// remoteInfo describes a file on a remote rclone backend.
type remoteInfo struct {
	Name    string
	Size    uint64
	ModTime time.Time
}

// SnapInfo provides snapshot metadata from an rclone file.
type SnapInfo interface {
	Version() snaptype.Version
	From() uint64
	To() uint64
	Type() snaptype.Type
}

// fileInfo adapts rcloneInfo to the fs.FileInfo interface.
type fileInfo struct {
	*rcloneInfo
}

func (fi *fileInfo) Name() string      { return fi.file }
func (fi *fileInfo) Size() int64       { return int64(fi.remoteInfo.Size) }
func (fi *fileInfo) Mode() fs.FileMode { return fs.ModeIrregular }
func (fi *fileInfo) ModTime() time.Time { return fi.remoteInfo.ModTime }
func (fi *fileInfo) IsDir() bool       { return false }
func (fi *fileInfo) Sys() any          { return fi.rcloneInfo }

// dirEntry adapts fileInfo to the fs.DirEntry interface.
type dirEntry struct {
	info *fileInfo
}

func (e dirEntry) Name() string               { return e.info.Name() }
func (e dirEntry) IsDir() bool                { return e.info.IsDir() }
func (e dirEntry) Type() fs.FileMode          { return e.info.Mode() }
func (e dirEntry) Info() (fs.FileInfo, error)  { return e.info, nil }

// RCloneOptions holds rclone bandwidth configuration.
type RCloneOptions struct {
	BwLimit     string `json:"BwLimit,omitempty"`
	BwLimitFile string `json:"BwLimitFile,omitempty"`
}

// RCloneTransferStats holds per-transfer statistics from rclone.
type RCloneTransferStats struct {
	Bytes      uint64  `json:"bytes"`
	Eta        uint    `json:"eta"`
	Group      string  `json:"group"`
	Name       string  `json:"name"`
	Percentage uint    `json:"percentage"`
	Size       uint64  `json:"size"`
	Speed      float64 `json:"speed"`
	SpeedAvg   float64 `json:"speedAvg"`
}

// RCloneStats holds aggregate statistics from rclone.
type RCloneStats struct {
	Bytes               uint64                `json:"bytes"`
	Checks              uint                  `json:"checks"`
	DeletedDirs         uint                  `json:"deletedDirs"`
	Deletes             uint                  `json:"deletes"`
	ElapsedTime         float64               `json:"elapsedTime"`
	Errors              uint                  `json:"errors"`
	Eta                 uint                  `json:"eta"`
	FatalError          bool                  `json:"fatalError"`
	Renames             uint                  `json:"renames"`
	RetryError          bool                  `json:"retryError"`
	ServerSideCopies    uint                  `json:"serverSideCopies"`
	ServerSideCopyBytes uint                  `json:"serverSideCopyBytes"`
	ServerSideMoveBytes uint                  `json:"serverSideMoveBytes"`
	ServerSideMoves     uint                  `json:"serverSideMoves"`
	Speed               float64               `json:"speed"`
	TotalBytes          uint64                `json:"totalBytes"`
	TotalChecks         uint                  `json:"totalChecks"`
	TotalTransfers      uint                  `json:"totalTransfers"`
	TransferTime        float64               `json:"transferTime"`
	Transferring        []RCloneTransferStats `json:"transferring"`
	Transfers           uint                  `json:"transfers"`
}

// rcloneFilter specifies file inclusion rules for rclone operations.
type rcloneFilter struct {
	IncludeRule []string `json:"IncludeRule"`
}

// rcloneFs describes an rclone remote filesystem.
type rcloneFs struct {
	Type    string `json:"type"`
	Url     string `json:"url,omitempty"`
	Headers string `json:"headers,omitempty"`
}

// rcloneRequest is the payload sent to rclone's HTTP API.
type rcloneRequest struct {
	Async     bool           `json:"_async,omitempty"`
	Config    *RCloneOptions `json:"_config,omitempty"`
	Group     string         `json:"_group"`
	SrcFs     interface{}    `json:"srcFs"`
	SrcRemote string         `json:"srcRemote,omitempty"`
	DstFs     string         `json:"dstFs"`
	DstRemote string         `json:"dstRemote,omitempty"`
	Filter    rcloneFilter   `json:"_filter"`
}

// RCloneClient manages a local rclone daemon process and its HTTP API.
type RCloneClient struct {
	rclone        *exec.Cmd
	rcloneUrl     string
	rcloneSession *http.Client
	logger        log.Logger
	bwLimit       *rate.Limit
	optionsQueue  chan RCloneOptions
}

var (
	rcClient     RCloneClient
	rcClientStart sync.Once
)

const connectionTimeout = time.Second * 5

// NewRCloneClient returns a singleton RCloneClient, starting the rclone daemon on first call.
func NewRCloneClient(logger log.Logger) (*RCloneClient, error) {
	var err error
	rcClientStart.Do(func() {
		err = rcClient.start(logger)
	})
	if err != nil {
		return nil, err
	}
	return &rcClient, nil
}

func (c *RCloneClient) start(logger log.Logger) error {
	c.logger = logger

	rclone, _ := exec.LookPath("rclone")
	if len(rclone) == 0 {
		return fmt.Errorf("rclone not found in PATH")
	}

	logger.Info("[downloader] rclone found in PATH: enhanced upload/download enabled")

	p, err := freePort()
	if err != nil {
		return fmt.Errorf("rclone: no free port: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	addr := fmt.Sprintf("127.0.0.1:%d", p)
	c.rclone = exec.CommandContext(ctx, rclone, "rcd", "--rc-addr", addr, "--rc-no-auth", "--multi-thread-streams", "1")
	c.rcloneUrl = "http://" + addr
	c.rcloneSession = &http.Client{}
	c.optionsQueue = make(chan RCloneOptions, 100)

	if err := c.rclone.Start(); err != nil {
		cancel()
		logger.Warn("[downloader] Uploading disabled: rclone didn't start", "err", err)
		return fmt.Errorf("rclone didn't start: %w", err)
	}
	logger.Info("[downloader] rclone started", "addr", addr)

	go func() {
		signalCh := make(chan os.Signal, 1)
		signal.Notify(signalCh, syscall.SIGTERM, syscall.SIGINT)
		for {
			select {
			case <-signalCh:
				cancel()
			case o := <-c.optionsQueue:
				c.setOptions(ctx, o)
			}
		}
	}()

	return nil
}

func (c *RCloneClient) ListRemotes(ctx context.Context) ([]string, error) {
	result, err := c.cmd(ctx, "config/listremotes", nil)
	if err != nil {
		return nil, err
	}

	remotes := struct {
		Remotes []string `json:"remotes"`
	}{}
	if err = json.Unmarshal(result, &remotes); err != nil {
		return nil, err
	}
	return remotes.Remotes, nil
}

func (c *RCloneClient) Stats(ctx context.Context) (*RCloneStats, error) {
	result, err := c.cmd(ctx, "core/stats", nil)
	if err != nil {
		return nil, err
	}

	var stats RCloneStats
	if err = json.Unmarshal(result, &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

func (c *RCloneClient) GetBwLimit() rate.Limit {
	if c.bwLimit != nil {
		return *c.bwLimit
	}
	return 0
}

func (c *RCloneClient) SetBwLimit(ctx context.Context, limit rate.Limit) {
	if c.bwLimit == nil || limit != *c.bwLimit {
		c.bwLimit = &limit
		bwLimit := datasize.ByteSize(limit).KBytes()
		c.logger.Trace("Setting rclone bw limit", "kbytes", int64(bwLimit))
		c.optionsQueue <- RCloneOptions{
			BwLimit: fmt.Sprintf("%dK", int64(bwLimit)),
		}
	}
}

func (c *RCloneClient) setOptions(ctx context.Context, options RCloneOptions) error {
	_, err := c.cmd(ctx, "options/set", struct {
		Main RCloneOptions `json:"main"`
	}{Main: options})
	return err
}

func (c *RCloneClient) sync(ctx context.Context, request *rcloneRequest) error {
	_, err := c.cmd(ctx, "sync/sync", request)
	return err
}

func (c *RCloneClient) copyFile(ctx context.Context, request *rcloneRequest) error {
	_, err := c.cmd(ctx, "operations/copyfile", request)
	return err
}

func (c *RCloneClient) cmd(ctx context.Context, path string, args interface{}) ([]byte, error) {
	var requestBodyReader io.Reader
	if args != nil {
		requestBody, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		requestBodyReader = bytes.NewBuffer(requestBody)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rcloneUrl+"/"+path, requestBodyReader)
	if err != nil {
		return nil, err
	}
	if requestBodyReader != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	ctx, cancel := context.WithTimeout(ctx, connectionTimeout)
	defer cancel()

	var response *http.Response
	err = retry(ctx, func(ctx context.Context) error {
		response, err = c.rcloneSession.Do(request) //nolint:bodyclose
		return err
	}, isConnectionError, time.Millisecond*200, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, c.handleCmdError(path, args, response)
	}
	return io.ReadAll(response.Body)
}

func (c *RCloneClient) handleCmdError(path string, args interface{}, response *http.Response) error {
	responseBody := struct {
		Error string `json:"error"`
	}{}

	argsJson := marshalArgsJSON(args)

	if err := json.NewDecoder(response.Body).Decode(&responseBody); err == nil && len(responseBody.Error) > 0 {
		c.logger.Warn("[downloader] rclone cmd failed", "path", path, "args", argsJson, "status", response.Status, "err", responseBody.Error)
		return fmt.Errorf("cmd: %s failed: %s: %s", path, response.Status, responseBody.Error)
	}

	c.logger.Warn("[downloader] rclone cmd failed", "path", path, "args", argsJson, "status", response.Status)
	return fmt.Errorf("cmd: %s failed: %s", path, response.Status)
}

func marshalArgsJSON(args interface{}) string {
	if args == nil {
		return ""
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(b)
}

// freePort returns an available TCP port on localhost.
func freePort() (int, error) {
	a, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", a)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func isConnectionError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	return false
}

func retry(ctx context.Context, op func(context.Context) error, isRecoverableError func(error) bool, delay time.Duration, lastErr error) error {
	err := op(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) && lastErr != nil {
		return lastErr
	}
	if !isRecoverableError(err) {
		return err
	}

	delayTimer := time.NewTimer(delay)
	select {
	case <-delayTimer.C:
		return retry(ctx, op, isRecoverableError, delay, err)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return err
		}
		return ctx.Err()
	}
}

// ErrAccessDenied is returned when the rclone remote denies access.
var ErrAccessDenied = errors.New("access denied")
