//go:build windows

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

const ctrlBreakEvent = 1

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole            = kernel32.NewProc("AttachConsole")
	procFreeConsole              = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}

func sendGracefulStop(pid int) error {
	// A process may only attach to one console. Detach the short-lived stop
	// command first, attach to the node console, and ignore the CTRL_BREAK in
	// this process while broadcasting it to the attached console.
	procFreeConsole.Call()
	attached, _, attachErr := procAttachConsole.Call(uintptr(uint32(pid)))
	if attached == 0 {
		return fmt.Errorf("AttachConsole(%d): %w", pid, attachErr)
	}
	defer procFreeConsole.Call()
	ignored, _, handlerErr := procSetConsoleCtrlHandler.Call(0, 1)
	if ignored == 0 {
		return fmt.Errorf("SetConsoleCtrlHandler: %w", handlerErr)
	}
	generated, _, generateErr := procGenerateConsoleCtrlEvent.Call(ctrlBreakEvent, 0)
	if generated == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", generateErr)
	}
	return nil
}
