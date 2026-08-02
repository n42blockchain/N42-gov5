//go:build !windows

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processStartTime returns pid's start time in clock ticks since boot, taken
// from field 22 of /proc/<pid>/stat. Process IDs are reused, so the ID alone
// cannot identify a process; the (id, start time) pair can. Returns false where
// /proc is unavailable, in which case the caller falls back to the ID alone.
func processStartTime(pid int) (int64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// The second field is the executable name in parentheses and may itself
	// contain spaces or parentheses, so split after its final ')'.
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data)[close+1:])
	// After the name, field 1 here is state, so start time (field 22 overall)
	// is index 19.
	if len(fields) <= 19 {
		return 0, false
	}
	v, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func sendGracefulStop(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}
