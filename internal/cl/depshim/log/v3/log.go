//go:build n42el

// Package v3 re-exports N42's lib/log/v3 directly at the upstream
// import path used by erigon's cl tree
// (github.com/erigontech/erigon/common/log/v3 → mapped to
//  github.com/n42blockchain/N42/internal/cl/depshim/log/v3).
package v3

import libLog "github.com/n42blockchain/N42/lib/log/v3"

type (
	Logger  = libLog.Logger
	Lvl     = libLog.Lvl
	Ctx     = libLog.Ctx
	Handler = libLog.Handler
	Format  = libLog.Format
	Lazy    = libLog.Lazy
)

var (
	Log              = libLog.Log
	New              = libLog.New
	Root             = libLog.Root
	Crit             = libLog.Crit
	Error            = libLog.Error
	Warn             = libLog.Warn
	Info             = libLog.Info
	Debug            = libLog.Debug
	Trace            = libLog.Trace
	LvlFilterHandler = libLog.LvlFilterHandler
	StdoutHandler    = libLog.StdoutHandler
	StderrHandler    = libLog.StderrHandler
	StreamHandler    = libLog.StreamHandler
	FileHandler      = libLog.FileHandler
	LogfmtFormat     = libLog.LogfmtFormat
	JSONFormat       = libLog.JsonFormat
	TerminalFormat   = libLog.TerminalFormat
)

const (
	LvlCrit  = libLog.LvlCrit
	LvlError = libLog.LvlError
	LvlWarn  = libLog.LvlWarn
	LvlInfo  = libLog.LvlInfo
	LvlDebug = libLog.LvlDebug
	LvlTrace = libLog.LvlTrace
)
