// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
)

const pidFileName = "n42.pid"

var errPIDFileInUse = errors.New("n42 node is already running")

var stopCommand = &cli.Command{
	Name:  "stop",
	Usage: "Gracefully stop a running n42 node",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "data.dir",
			Aliases: []string{"datadir"},
			Usage:   "Data directory containing n42.pid",
			Value:   DefaultDataDir,
		},
		&cli.IntFlag{
			Name:  "pid",
			Usage: "Process ID to stop (overrides --data.dir PID lookup)",
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Usage: "Maximum time to wait for graceful shutdown",
			Value: 120 * time.Second,
		},
	},
	Action: stopNode,
}

func pidFilePath(dataDir string) string {
	return filepath.Join(dataDir, pidFileName)
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid PID file %s", path)
	}
	return pid, nil
}

// acquirePIDFile creates the node PID file exclusively. A live residual PID is
// a double-start error; a dead PID is stale and is replaced. The returned
// cleanup removes the file only while it still belongs to this process.
func acquirePIDFile(dataDir string, pid int) (func() error, error) {
	if dataDir == "" {
		return func() error { return nil }, nil
	}
	if pid <= 0 {
		return nil, fmt.Errorf("invalid process ID %d", pid)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory for PID file: %w", err)
	}
	path := pidFilePath(dataDir)
	for attempts := 0; attempts < 3; attempts++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if _, err = fmt.Fprintf(file, "%d\n", pid); err == nil {
				err = file.Sync()
			}
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("write PID file: %w", err)
			}
			return func() error { return removeOwnedPIDFile(path, pid) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create PID file: %w", err)
		}

		existingPID, readErr := readPIDFile(path)
		if readErr == nil && processAlive(existingPID) {
			return nil, fmt.Errorf("%w: pid %d (data directory %s)", errPIDFileInUse, existingPID, dataDir)
		}
		if readErr != nil {
			// Do not delete a just-created empty file from a concurrent starter.
			info, statErr := os.Stat(path)
			if statErr == nil && time.Since(info.ModTime()) < 5*time.Second {
				return nil, fmt.Errorf("PID file is being initialized: %s", path)
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale PID file: %w", err)
		}
	}
	return nil, fmt.Errorf("could not acquire PID file %s", path)
}

func removeOwnedPIDFile(path string, pid int) error {
	owner, err := readPIDFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner != pid {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func stopNode(ctx *cli.Context) error {
	timeout := ctx.Duration("timeout")
	if timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	pid := ctx.Int("pid")
	dataDir := ctx.String("data.dir")
	fromFile := pid <= 0
	if fromFile {
		var err error
		pid, err = readPIDFile(pidFilePath(dataDir))
		if err != nil {
			return fmt.Errorf("read node PID: %w", err)
		}
	}
	if pid == os.Getpid() {
		return fmt.Errorf("refusing to stop the current process")
	}
	if !processAlive(pid) {
		if fromFile {
			_ = removeOwnedPIDFile(pidFilePath(dataDir), pid)
		}
		return fmt.Errorf("n42 process %d is not running", pid)
	}
	if err := sendGracefulStop(pid); err != nil {
		return fmt.Errorf("signal n42 process %d: %w", pid, err)
	}
	fmt.Printf("Sent graceful stop to n42 process %d; waiting up to %s...\n", pid, timeout)
	if err := waitForProcessExit(pid, timeout); err != nil {
		return err
	}
	if fromFile {
		_ = removeOwnedPIDFile(pidFilePath(dataDir), pid)
	}
	fmt.Printf("n42 process %d stopped gracefully.\n", pid)
	return nil
}

func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !processAlive(pid) {
				return nil
			}
		case <-deadline.C:
			return fmt.Errorf("timed out after %s waiting for process %d; process was not terminated", timeout, pid)
		}
	}
}
