// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Log unit for the log package.
// Declares the Logger and Lvl type aliases.
// Logging adapter shims.

//go:build n42el

// Package log re-exports N42's lib/log/v3 under the import path expected by
// the Caplin source tree (github.com/n42blockchain/N42/internal/cl/depshim/log).

package log

import libLog "github.com/n42blockchain/N42/lib/log/v3"

// Logger is the interface alias.
type Logger = libLog.Logger

// Common helper functions Caplin uses.
var (
	Debug = libLog.Debug
	Info  = libLog.Info
	Warn  = libLog.Warn
	Error = libLog.Error
	Trace = libLog.Trace
	Crit  = libLog.Crit
	Root  = libLog.Root
	New   = libLog.New
)

// Lvl re-exports.
type Lvl = libLog.Lvl

const (
	LvlCrit  = libLog.LvlCrit
	LvlError = libLog.LvlError
	LvlWarn  = libLog.LvlWarn
	LvlInfo  = libLog.LvlInfo
	LvlDebug = libLog.LvlDebug
	LvlTrace = libLog.LvlTrace
)
