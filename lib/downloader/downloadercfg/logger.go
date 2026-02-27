/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package downloadercfg

import (
	"fmt"
	"strings"

	lg "github.com/anacrolix/log"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func init() {
	lg.Default.Handlers = []lg.Handler{adapterHandler{}}
}

func Int2LogLevel(level int) (lvl lg.Level, dbg bool, err error) {
	switch level {
	case 0:
		lvl = lg.Critical
	case 1:
		lvl = lg.Error
	case 2:
		lvl = lg.Warning
	case 3:
		lvl = lg.Info
	case 4:
		lvl = lg.Debug
	case 5:
		lvl = lg.Debug
		dbg = true
	default:
		return lvl, dbg, fmt.Errorf("invalid level set, expected a number between 0-5 but got: %d", level)
	}
	return lvl, dbg, nil
}

type noopHandler struct{}

func (b noopHandler) Handle(r lg.Record) {}

type adapterHandler struct{}

// skipPatterns lists substrings that cause a log record to be demoted to Trace level.
var skipPatterns = map[lg.Level][]string{
	lg.Debug: {
		"completion change", "hashed piece", "set torrent=",
		"all initial dials failed", "local and remote peer ids are the same",
		"connection at", "don't want conns right now", "is mutually complete",
		"sending PEX message", "received pex message",
		"announce to", "announcing to",
		"EOF", "closed", "connection reset by peer",
		"use of closed network connection", "broken pipe",
		"inited with remoteAddr",
	},
	lg.Info: {"EOF"},
	lg.Warning: {
		"EOF", "requested chunk too long", "banned ip",
		"banning webseed", "TrackerClient closed",
		"being sole dirtier of piece", "webrtc conn for unloaded torrent",
		"could not find offer for id", "received invalid reject",
		"reservation cancelled",
	},
	lg.Error: {"EOF", "short write", "disabling data download"},
	lg.Critical: {"EOF", "torrent closed", "don't want conns"},
}

func shouldSkip(str string, lvl lg.Level) bool {
	patterns, ok := skipPatterns[lvl]
	if !ok {
		// Unknown levels: skip EOF and webseed errors
		return strings.Contains(str, "EOF") ||
			strings.Contains(str, "unhandled response status") ||
			strings.Contains(str, "error doing webseed request")
	}
	for _, p := range patterns {
		if strings.Contains(str, p) {
			return true
		}
	}
	return false
}

func (b adapterHandler) Handle(r lg.Record) {
	str := r.String()
	if shouldSkip(str, r.Level) {
		log.Trace(str, "lvl", r.Level.LogString())
		return
	}

	switch r.Level {
	case lg.Debug:
		log.Debug(str)
	case lg.Info:
		log.Info(str)
	case lg.Warning:
		log.Warn(str)
	case lg.Error, lg.Critical:
		log.Error(str)
	default:
		log.Info("[downloader] "+str, "torrent_log_type", "unknown", "or", r.Level.LogString())
	}
}
