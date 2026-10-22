package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"
)

// Explicit file-only diagnostics. There is no listener, and an existing output
// directory is never reused. The caller closes this after the server drains.
type privateOwnerProfiles struct {
	dir      string
	cpu      *os.File
	started  time.Time
	oldMutex int
	once     sync.Once
	closeErr error
}

func startPrivateOwnerProfiles(dir string) (*privateOwnerProfiles, error) {
	if dir == "" {
		return nil, nil
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("profile directory must be an absolute clean path")
	}
	parent := filepath.Dir(dir)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return nil, errors.New("profile parent must exist without aliases")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, errors.New("profile parent must be an owned private directory")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("fresh profile directory required: %w", err)
	}
	cpu, err := os.OpenFile(filepath.Join(dir, "cpu.pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(cpu); err != nil {
		return nil, errors.Join(err, cpu.Close())
	}
	p := &privateOwnerProfiles{dir: dir, cpu: cpu, started: time.Now()}
	runtime.SetBlockProfileRate(1_000_000)
	p.oldMutex = runtime.SetMutexProfileFraction(5)
	return p, nil
}

func (p *privateOwnerProfiles) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		stopped := time.Now() // conservative common CPU/block capture end
		pprof.StopCPUProfile()
		runtime.SetBlockProfileRate(0)
		runtime.SetMutexProfileFraction(p.oldMutex)
		p.closeErr = errors.Join(p.cpu.Sync(), p.cpu.Close())
		for _, name := range []string{"block", "mutex"} {
			file, err := os.OpenFile(filepath.Join(p.dir, name+".pprof"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				p.closeErr = errors.Join(p.closeErr, err)
				continue
			}
			err = pprof.Lookup(name).WriteTo(file, 0)
			p.closeErr = errors.Join(p.closeErr, err, file.Sync(), file.Close())
		}
		if p.closeErr != nil {
			return // A completed manifest never describes an unfinished profile.
		}
		type profileFile struct {
			Path   string `json:"path"`
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
		}
		files := make([]profileFile, 0, 3)
		for _, name := range []string{"cpu", "block", "mutex"} {
			path := filepath.Join(p.dir, name+".pprof")
			file, err := os.Open(path)
			if err != nil {
				p.closeErr = err
				return
			}
			hash := sha256.New()
			n, readErr := io.Copy(hash, file)
			p.closeErr = errors.Join(readErr, file.Close())
			if p.closeErr != nil {
				return
			}
			files = append(files, profileFile{path, n, hex.EncodeToString(hash.Sum(nil))})
		}
		manifest, err := os.OpenFile(filepath.Join(p.dir, "profile-session.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			p.closeErr = err
			return
		}
		err = json.NewEncoder(manifest).Encode(struct {
			Completed      bool          `json:"completed"`
			StartedUnixNS  int64         `json:"started_unix_ns"`
			FinishedUnixNS int64         `json:"finished_unix_ns"`
			GOMAXPROCS     int           `json:"gomaxprocs"`
			BlockRateNS    int           `json:"block_rate_ns"`
			MutexFraction  int           `json:"mutex_fraction"`
			Files          []profileFile `json:"files"`
		}{true, p.started.UnixNano(), stopped.UnixNano(), runtime.GOMAXPROCS(0), 1_000_000, 5, files})
		p.closeErr = errors.Join(err, manifest.Sync(), manifest.Close())
	})
	return p.closeErr
}
