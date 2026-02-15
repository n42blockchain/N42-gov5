// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package log

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	sshterminal "golang.org/x/crypto/ssh/terminal"
)

// ANSI color codes
const (
	Reset = "\033[0m"
	Bold  = "\033[1m"
	Dim   = "\033[2m"

	// Foreground colors
	Black   = "\033[30m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"

	// Bright foreground colors
	BrightBlack   = "\033[90m"
	BrightRed     = "\033[91m"
	BrightGreen   = "\033[92m"
	BrightYellow  = "\033[93m"
	BrightBlue    = "\033[94m"
	BrightMagenta = "\033[95m"
	BrightCyan    = "\033[96m"
	BrightWhite   = "\033[97m"

	// Background colors
	BgRed     = "\033[41m"
	BgGreen   = "\033[42m"
	BgYellow  = "\033[43m"
	BgBlue    = "\033[44m"
	BgMagenta = "\033[45m"
	BgCyan    = "\033[46m"
)

// Module colors - Claude Code style colored dots
var moduleColors = map[string]string{
	"node":         BrightCyan,
	"p2p":          BrightMagenta,
	"sync":         BrightGreen,
	"initial-sync": BrightGreen,
	"api":          BrightBlue,
	"rpc":          BrightBlue,
	"txspool":      BrightYellow,
	"miner":        Yellow,
	"consensus":    Magenta,
	"apos":         Magenta,
	"deposit":      Cyan,
	"enode":        BrightMagenta,
	"main":         BrightWhite,
	"blockchain":   BrightCyan,
	"db":           Blue,
	"downloader":   Green,
}

// Symbols for visual hierarchy and log levels
const (
	DotSymbol   = "●" // Info indicator
	WarnSymbol  = "!" // Warn indicator
	ErrorSymbol = "✖" // Error indicator
	DebugSymbol = "·" // Debug indicator
	TraceSymbol = "‥" // Trace indicator
	SubSymbol   = "⎿" // Sub-item indicator
	ArrowSymbol = "→" // Flow indicator
	CheckSymbol = "✓" // Success
	CrossSymbol = "✗" // Failure
)

// PrettyFormatter implements a modern CLI style log formatter
type PrettyFormatter struct {
	// ForceColors forces colored output even when not a TTY
	ForceColors bool

	// DisableColors disables colored output
	DisableColors bool

	// DisableTimestamp disables timestamp output
	DisableTimestamp bool

	// ShowModule shows the module/prefix name
	ShowModule bool

	// startTime for relative time calculation
	startTime time.Time

	// lastAbsoluteTime tracks when we last showed absolute time
	lastAbsoluteTime time.Time

	// lastTimeStr tracks the last displayed timestamp to avoid duplicates
	lastTimeStr string

	// isTerminal caches TTY check
	isTerminal bool

	sync.Once
}

// NewPrettyFormatter creates a new PrettyFormatter with sensible defaults
func NewPrettyFormatter() *PrettyFormatter {
	now := time.Now()
	return &PrettyFormatter{
		ShowModule:       true,
		startTime:        now,
		lastAbsoluteTime: now.Add(-10 * time.Minute), // Force first log to show absolute time
	}
}

func (f *PrettyFormatter) init(entry *logrus.Entry) {
	if entry.Logger != nil {
		f.isTerminal = f.checkIfTerminal(entry.Logger.Out)
	}
}

func (f *PrettyFormatter) checkIfTerminal(w io.Writer) bool {
	switch v := w.(type) {
	case *os.File:
		return sshterminal.IsTerminal(int(v.Fd()))
	default:
		return false
	}
}

// Format implements logrus.Formatter
// Format: ● module message key=value                    5s
func (f *PrettyFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	f.Do(func() { f.init(entry) })

	var b bytes.Buffer

	// If a progress bar is active, push it down with a newline first
	printMu.Lock()
	if progressActive {
		b.WriteString("\n")
		progressActive = false
	}
	printMu.Unlock()

	isColored := (f.ForceColors || f.isTerminal) && !f.DisableColors

	// Get module/prefix
	module := ""
	if prefix, ok := entry.Data["prefix"]; ok {
		module = fmt.Sprintf("%v", prefix)
	}

	// Format level indicator (colored symbol)
	levelIndicator := f.formatLevelIndicator(entry.Level, module, isColored)

	// Format message
	message := f.formatMessage(entry.Message, entry.Level, isColored)

	// Format fields
	fields := f.formatFields(entry.Data, entry.Level, isColored)

	// Format timestamp (at the end, in blue)
	timestamp := f.formatTime(entry.Time, isColored)

	// Build output: symbol module message fields    timestamp
	b.WriteString(levelIndicator)
	b.WriteString(" ")
	b.WriteString(message)

	if fields != "" {
		b.WriteString(" ")
		b.WriteString(fields)
	}

	// Add timestamp at the end
	if !f.DisableTimestamp && timestamp != "" {
		b.WriteString("  ")
		b.WriteString(timestamp)
	}

	b.WriteString("\n")

	return b.Bytes(), nil
}

// formatTime formats the timestamp - shown at end of line
// Shows relative time (5s, 2m30s) and absolute time every 10 minutes
// Only shows relative time when it differs from the last displayed time
// First log shows full time: 2026-01-02 15:04:05
// Every 10 minutes shows short time: 02-03 15:23:03
func (f *PrettyFormatter) formatTime(t time.Time, colored bool) string {
	elapsed := t.Sub(f.startTime)

	// Build relative time string (always show seconds precision)
	relativeStr := f.formatDuration(elapsed)

	// Check if we need to show absolute time (every 10 minutes)
	var absoluteStr string
	if t.Sub(f.lastAbsoluteTime) >= 10*time.Minute {
		// First time (elapsed < 1s) shows full format, subsequent shows short format
		if elapsed < time.Second {
			absoluteStr = t.Format("2006-01-02 15:04:05")
		} else {
			absoluteStr = t.Format("01-02 15:04:05")
		}
		f.lastAbsoluteTime = t
	}

	// Build result: absolute time always shown if present, relative time only if changed
	var result string
	if absoluteStr != "" {
		result = absoluteStr + " " + relativeStr
		f.lastTimeStr = relativeStr
	} else if relativeStr != f.lastTimeStr {
		result = relativeStr
		f.lastTimeStr = relativeStr
	}

	if result == "" {
		return ""
	}

	if colored {
		// Use muted blue-purple color for timestamp
		timeColor := "\033[38;5;60m" // Dark slate blue
		return fmt.Sprintf("%s%s%s", timeColor, result, Reset)
	}
	return result
}

// formatDuration formats a duration in compact form with seconds precision
// Examples: 0s, 59s, 1m0s, 2m30s, 1h5m2s, 2d20h15m59s
func (f *PrettyFormatter) formatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60

	var result string
	if days > 0 {
		result += fmt.Sprintf("%dd", days)
	}
	if hours > 0 || days > 0 {
		result += fmt.Sprintf("%dh", hours)
	}
	if mins > 0 || hours > 0 || days > 0 {
		result += fmt.Sprintf("%dm", mins)
	}
	result += fmt.Sprintf("%ds", secs)

	return result
}

// formatLevelIndicator returns a colored symbol based on level and module
// ● info (colored by module)
// ✻ warn (yellow)
// ✖ error (red)
// · debug (dim)
// ‥ trace (dim)
func (f *PrettyFormatter) formatLevelIndicator(level logrus.Level, module string, colored bool) string {
	var symbol string
	var levelColor string

	switch level {
	case logrus.ErrorLevel, logrus.FatalLevel, logrus.PanicLevel:
		symbol = ErrorSymbol
		levelColor = BrightRed
	case logrus.WarnLevel:
		symbol = WarnSymbol
		levelColor = BrightYellow
	case logrus.InfoLevel:
		symbol = DotSymbol
		// Use module color for info level
		if moduleColor, ok := moduleColors[module]; ok {
			levelColor = moduleColor
		} else {
			levelColor = BrightCyan
		}
	case logrus.DebugLevel:
		symbol = DebugSymbol
		levelColor = BrightBlack
	default:
		symbol = TraceSymbol
		levelColor = BrightBlack
	}

	if !colored {
		if f.ShowModule && module != "" {
			return fmt.Sprintf("%s %s", symbol, module)
		}
		return symbol
	}

	// Format: colored symbol + module name
	if f.ShowModule && module != "" {
		return fmt.Sprintf("%s%s%s %s%s%s", levelColor, symbol, Reset, Dim, module, Reset)
	}
	return fmt.Sprintf("%s%s%s", levelColor, symbol, Reset)
}

// formatMessage formats the log message
func (f *PrettyFormatter) formatMessage(msg string, level logrus.Level, colored bool) string {
	if !colored {
		return msg
	}

	// Highlight certain keywords
	msg = f.highlightKeywords(msg)

	switch level {
	case logrus.ErrorLevel, logrus.FatalLevel, logrus.PanicLevel:
		return fmt.Sprintf("%s%s%s", BrightRed, msg, Reset)
	case logrus.WarnLevel:
		return fmt.Sprintf("%s%s%s", BrightYellow, msg, Reset)
	default:
		return msg
	}
}

// highlightKeywords highlights important keywords in messages
func (f *PrettyFormatter) highlightKeywords(msg string) string {
	// Highlight success keywords (green)
	successKeywords := []string{"started", "connected", "initialized", "ready", "completed", "success", "synced", "stopped"}
	for _, kw := range successKeywords {
		if strings.Contains(strings.ToLower(msg), kw) {
			msg = strings.ReplaceAll(msg, kw, fmt.Sprintf("%s%s%s", BrightGreen, kw, Reset))
			msg = strings.ReplaceAll(msg, strings.Title(kw), fmt.Sprintf("%s%s%s", BrightGreen, strings.Title(kw), Reset))
		}
	}

	// Highlight failure keywords (red) - these override success keywords if both present
	failKeywords := []string{"failed", "error", "disconnected", "timeout", "mismatch"}
	for _, kw := range failKeywords {
		if strings.Contains(strings.ToLower(msg), kw) {
			msg = strings.ReplaceAll(msg, kw, fmt.Sprintf("%s%s%s", BrightRed, kw, Reset))
			msg = strings.ReplaceAll(msg, strings.Title(kw), fmt.Sprintf("%s%s%s", BrightRed, strings.Title(kw), Reset))
		}
	}

	return msg
}

// formatFields formats the key-value fields
func (f *PrettyFormatter) formatFields(data logrus.Fields, level logrus.Level, colored bool) string {
	if len(data) == 0 {
		return ""
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(data))
	for k := range data {
		if k != "prefix" { // Skip prefix as it's shown in the indicator
			keys = append(keys, k)
		}
	}

	if len(keys) == 0 {
		return ""
	}

	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		v := data[k]

		// Format value
		var valStr string
		switch val := v.(type) {
		case string:
			// Don't truncate important fields like multiAddr
			noTruncate := k == "multiAddr" || k == "enode"
			// Truncate long strings (except important fields)
			if len(val) > 60 && !noTruncate {
				valStr = val[:57] + "..."
			} else {
				valStr = val
			}
		case error:
			valStr = val.Error()
		default:
			valStr = fmt.Sprintf("%v", val)
		}

		if colored {
			// Special coloring for certain keys
			var keyColor, valColor string
			switch k {
			case "err", "error":
				keyColor = BrightRed
				valColor = Red
			case "peer", "peer_id", "id":
				keyColor = BrightMagenta
				valColor = Magenta
			case "block", "blockNr", "number", "height":
				keyColor = BrightCyan
				valColor = Cyan
			case "hash", "genesis":
				keyColor = BrightBlue
				valColor = Blue
			default:
				keyColor = Dim
				valColor = ""
			}

			if valColor != "" {
				parts = append(parts, fmt.Sprintf("%s%s%s=%s%s%s", keyColor, k, Reset, valColor, valStr, Reset))
			} else {
				parts = append(parts, fmt.Sprintf("%s%s%s=%s", keyColor, k, Reset, valStr))
			}
		} else {
			parts = append(parts, fmt.Sprintf("%s=%s", k, valStr))
		}
	}

	return strings.Join(parts, " ")
}

// PrintBanner prints a beautiful startup banner with logo on left, info on right.
// System info (os, go, cpu, dataDir) is displayed compactly on the last two logo lines.
func PrintBanner(version, chain, consensus, genesis string, blockNum uint64, osInfo, goVersion string, cpuCores int, dataDir string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	// Colors - similar to Claude Code robot color (terracotta/brick red)
	logoColor := "\033[38;5;173m" // Terracotta/brick color
	dimColor := BrightBlack
	valueColor := BrightWhite

	// N42 solid block logo (compact 4-line version)
	logo := []string{
		"███╗   ██╗██╗  ██╗██████╗ ",
		"████╗  ██║██║  ██║╚════██╗",
		"██╔██╗ ██║███████║ █████╔╝",
		"██║╚██╗██║╚════██║██╔═══╝ ",
		"██║ ╚████║     ██║███████╗",
		"╚═╝  ╚═══╝     ╚═╝╚══════╝",
	}

	// Info lines to display on right side of logo
	infoLines := []string{
		fmt.Sprintf("%sv%s%s %s·%s %s%s%s", valueColor, version, Reset, dimColor, Reset, dimColor, chain, Reset),
		fmt.Sprintf("%s%s%s", dimColor, consensus, Reset),
		fmt.Sprintf("%sGenesis %s%s", dimColor, truncateHash(genesis), Reset),
		fmt.Sprintf("%sBlock   #%s%d%s", dimColor, valueColor, blockNum, Reset),
		fmt.Sprintf("%s%s %s·%s %s %s·%s %d cores", dimColor, osInfo, dimColor, Reset+dimColor, goVersion, dimColor, Reset+dimColor, cpuCores),
		fmt.Sprintf("%s%s%s", dimColor, dataDir, Reset),
	}

	fmt.Println()

	// Print logo with info on right side
	for i, logoLine := range logo {
		info := ""
		if i < len(infoLines) {
			info = infoLines[i]
		}
		fmt.Printf("  %s%s%s  %s\n", logoColor, logoLine, Reset, info)
	}

	fmt.Println()
}

// PrintStartupProgress prints startup progress items
func PrintStartupProgress(step int, total int, message string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("  %s-%s %s[%d/%d]%s Starting %s%s%s...\n",
		BrightCyan, Reset,
		Dim, step, total, Reset,
		BrightWhite, message, Reset)
}

// PrintSubItem prints a sub-item with hierarchy indicator
func PrintSubItem(message string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("  %s%s%s %s\n", Dim, SubSymbol, Reset, message)
}

// PrintSuccess prints a success message
func PrintSuccess(message string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("  %s%s%s %s%s%s\n", BrightGreen, CheckSymbol, Reset, BrightGreen, message, Reset)
}

// PrintError prints an error message
func PrintError(message string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("  %s%s%s %s%s%s\n", BrightRed, CrossSymbol, Reset, BrightRed, message, Reset)
}

// PrintWarning prints a warning message
func PrintWarning(message string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("  %s%s%s %s%s%s\n", BrightYellow, WarnSymbol, Reset, BrightYellow, message, Reset)
}

// PrintShutdownBanner prints a graceful shutdown message
func PrintShutdownBanner() {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Println()
	fmt.Printf("  %s╭───────────────────────────────────────────────────────╮%s\n", BrightYellow, Reset)
	fmt.Printf("  %s│%s  %sGraceful shutdown initiated%s %s(Ctrl+C to force quit)%s   %s│%s\n",
		BrightYellow, Reset, BrightWhite, Reset, Dim, Reset, BrightYellow, Reset)
	fmt.Printf("  %s╰───────────────────────────────────────────────────────╯%s\n", BrightYellow, Reset)
	fmt.Println()
}

// PrintShutdownStep prints a shutdown progress step
func PrintShutdownStep(step int, total int, service string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("  %s-%s %s[%d/%d]%s Stopping %s%s%s...\n",
		BrightYellow, Reset,
		Dim, step, total, Reset,
		BrightWhite, service, Reset)
}

// PrintShutdownComplete prints shutdown complete message
func PrintShutdownComplete() {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("  %s%s%s %sAll services stopped%s\n", BrightGreen, CheckSymbol, Reset, BrightGreen, Reset)
	fmt.Println()
}

// truncateHash truncates a hash string for display
func truncateHash(hash string) string {
	if len(hash) <= 20 {
		return hash
	}
	// Show first 8 and last 8 characters
	return hash[:10] + "..." + hash[len(hash)-8:]
}

// FormatPeerID formats a peer ID for display
func FormatPeerID(peerID string) string {
	if len(peerID) <= 20 {
		return peerID
	}
	return peerID[:16] + "..."
}

// FormatDuration formats a duration in a friendly way
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	} else if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	} else if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", mins, secs)
	} else {
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
}

// FormatBytes formats bytes in a human-readable way
func FormatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// WaitingSpinner prints a waiting message with progress
func PrintWaiting(message string, current, required int) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()
	fmt.Printf("\r  %s%s%s %s %s(%d/%d)%s",
		BrightCyan, DotSymbol, Reset,
		message,
		Dim, current, required, Reset)
}

// printMu protects all Print* functions for concurrent safety.
var printMu sync.Mutex

// progressActive tracks whether the last output was a \r progress bar line.
// Other Print* functions check this to emit a \n first, avoiding collisions.
var progressActive bool

// Package-level time tracker for progress bar (mirrors PrettyFormatter rules).
var (
	progStartTime    time.Time
	progLastAbsTime  time.Time
	progLastTimeStr  string
)

// formatProgressTime formats a timestamp for the progress bar, following
// the same rules as PrettyFormatter.formatTime: relative time shown when
// it changes (per-second), absolute time appended every 10 minutes.
func formatProgressTime() string {
	now := time.Now()

	// Initialize on first call
	if progStartTime.IsZero() {
		progStartTime = now
		progLastAbsTime = now.Add(-10 * time.Minute) // force first absolute
	}

	elapsed := now.Sub(progStartTime)

	// Relative time
	relativeStr := formatCompactDuration(elapsed)

	// Absolute time every 10 minutes
	var absoluteStr string
	if now.Sub(progLastAbsTime) >= 10*time.Minute {
		if elapsed < time.Second {
			absoluteStr = now.Format("2006-01-02 15:04:05")
		} else {
			absoluteStr = now.Format("01-02 15:04:05")
		}
		progLastAbsTime = now
	}

	var result string
	if absoluteStr != "" {
		result = absoluteStr + " " + relativeStr
		progLastTimeStr = relativeStr
	} else if relativeStr != progLastTimeStr {
		result = relativeStr
		progLastTimeStr = relativeStr
	}

	if result == "" {
		return ""
	}

	timeColor := "\033[38;5;60m"
	return fmt.Sprintf("  %s%s%s", timeColor, result, Reset)
}

// formatCompactDuration formats a duration identically to PrettyFormatter.formatDuration.
func formatCompactDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60

	var result string
	if days > 0 {
		result += fmt.Sprintf("%dd", days)
	}
	if hours > 0 || days > 0 {
		result += fmt.Sprintf("%dh", hours)
	}
	if mins > 0 || hours > 0 || days > 0 {
		result += fmt.Sprintf("%dm", mins)
	}
	result += fmt.Sprintf("%ds", secs)
	return result
}

// clearProgressLine prints a newline if a \r progress bar is active,
// so subsequent output starts on a clean line. Caller must hold printMu.
func clearProgressLine() {
	if progressActive {
		fmt.Println()
		progressActive = false
	}
}

// maxDisplayWidth caps the visual width for dividers and sections.
const maxDisplayWidth = 80

// getTerminalWidth returns a capped terminal width for visual elements.
func getTerminalWidth() int {
	w, _, err := sshterminal.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 70
	}
	if w > maxDisplayWidth {
		return maxDisplayWidth
	}
	return w
}

// isTerminalOut returns true if stdout is a terminal.
func isTerminalOut() bool {
	return sshterminal.IsTerminal(int(os.Stdout.Fd()))
}

// PrintSection prints a section title with a decorative line.
// Example: ── Blockchain ──────────────────────────────
func PrintSection(title string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()

	w := getTerminalWidth()
	prefix := "── "
	suffix := " "
	// remaining width for dashes after title
	used := 2 + len(prefix) + len(title) + len(suffix) // 2 for leading spaces
	remaining := w - used
	if remaining < 4 {
		remaining = 4
	}
	dashes := strings.Repeat("─", remaining)

	fmt.Printf("\n  %s%s%s%s%s %s%s%s\n",
		Dim, prefix, Reset,
		BrightWhite, title, Reset,
		Dim, dashes+Reset)
}

// PrintDivider prints a horizontal divider line.
func PrintDivider() {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()

	w := getTerminalWidth()
	lineLen := w - 4
	if lineLen < 10 {
		lineLen = 10
	}
	fmt.Printf("  %s%s%s\n", Dim, strings.Repeat("─", lineLen), Reset)
}

// PrintKeyValue prints aligned key-value pairs.
func PrintKeyValue(pairs [][2]string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()

	// Find max key width for alignment
	maxKey := 0
	for _, p := range pairs {
		if len(p[0]) > maxKey {
			maxKey = len(p[0])
		}
	}

	for _, p := range pairs {
		padding := strings.Repeat(" ", maxKey-len(p[0])+2)
		fmt.Printf("    %s%s%s%s%s%s%s\n",
			Dim, p[0], Reset,
			padding,
			BrightWhite, p[1], Reset)
	}
}

// PrintSystemInfo prints a system information block with section header.
func PrintSystemInfo(osInfo, goVersion string, cpuCores int, dataDir string) {
	PrintSection("System")
	PrintKeyValue([][2]string{
		{"OS", osInfo},
		{"Go", goVersion},
		{"CPU Cores", fmt.Sprintf("%d", cpuCores)},
		{"Data Dir", dataDir},
	})
}

// PrintProgressBar prints a progress bar that refreshes in-place on terminals.
// Example: ● Syncing  ████████████░░░░░░░░░░░░░░░░░░  42.3%  ▸ 500 blk/s  ▸ ETA 5h58m  ▸ 3 peers
func PrintProgressBar(label string, current, total uint64, rate float64, eta string, peers int) {
	printMu.Lock()
	defer printMu.Unlock()

	const barWidth = 30

	progress := float64(0)
	if total > 0 {
		progress = float64(current) / float64(total)
	}
	if progress > 1 {
		progress = 1
	}

	filled := int(progress * barWidth)
	// Show at least 1 filled block when there is any progress
	if current > 0 && filled == 0 {
		filled = 1
	}
	empty := barWidth - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	pct := progress * 100

	// Use module color for sync
	color := BrightGreen
	if c, ok := moduleColors["sync"]; ok {
		color = c
	}

	peerWord := "peers"
	if peers == 1 {
		peerWord = "peer"
	}

	timeStr := formatProgressTime()

	line := fmt.Sprintf("%s%s%s %s  %s%s%s  %s%.1f%%%s  %s▸%s %.0f blk/s  %s▸%s ETA %s  %s▸%s %d %s%s",
		color, DotSymbol, Reset,
		label,
		Cyan, bar, Reset,
		BrightWhite, pct, Reset,
		Dim, Reset,
		rate,
		Dim, Reset,
		eta,
		Dim, Reset,
		peers, peerWord,
		timeStr,
	)

	if isTerminalOut() {
		// \033[K clears from cursor to end of line, preventing leftover characters
		fmt.Printf("\r%s\033[K", line)
		progressActive = true
	} else {
		fmt.Printf("%s\n", line)
	}
}

// PrintStatusLine prints a compact status line for a module.
// Example: ● p2p  3 peers (in:1 out:2) ▸ active:2
func PrintStatusLine(module, metrics string) {
	printMu.Lock()
	defer printMu.Unlock()

	// Skip when progress bar is active — peer info is already shown there
	if progressActive {
		return
	}

	color := BrightCyan
	if c, ok := moduleColors[module]; ok {
		color = c
	}

	fmt.Printf("%s%s%s %s%s%s  %s\n",
		color, DotSymbol, Reset,
		Dim, module, Reset,
		metrics)
}

// PrintErrorBox prints an error message in a bordered box.
func PrintErrorBox(title string, details []string) {
	printMu.Lock()
	defer printMu.Unlock()
	clearProgressLine()

	// Calculate box width
	maxLen := len(title) + 4 // "  ✖ " prefix
	for _, d := range details {
		if len(d)+4 > maxLen {
			maxLen = len(d) + 4
		}
	}
	boxWidth := maxLen + 6 // padding
	if boxWidth < 40 {
		boxWidth = 40
	}

	hLine := strings.Repeat("─", boxWidth-2)
	emptyLine := strings.Repeat(" ", boxWidth-2)

	fmt.Println()
	fmt.Printf("  %s╭%s╮%s\n", BrightRed, hLine, Reset)
	fmt.Printf("  %s│%s  %s%s %s%s%s%s│%s\n",
		BrightRed, Reset,
		BrightRed, ErrorSymbol, BrightWhite, title,
		strings.Repeat(" ", boxWidth-2-4-len(title)),
		BrightRed, Reset)
	fmt.Printf("  %s│%s%s%s│%s\n", BrightRed, Reset, emptyLine, BrightRed, Reset)
	for _, d := range details {
		padding := boxWidth - 2 - 4 - len(d)
		if padding < 0 {
			padding = 0
		}
		fmt.Printf("  %s│%s    %s%s%s%s│%s\n",
			BrightRed, Reset,
			Red, d,
			strings.Repeat(" ", padding),
			BrightRed, Reset)
	}
	fmt.Printf("  %s╰%s╯%s\n", BrightRed, hLine, Reset)
	fmt.Println()
}
