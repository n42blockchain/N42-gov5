// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Default data-directory resolution for the n42 node. DefaultDataDir
// returns ~/Library/n42 on macOS, %LOCALAPPDATA%\n42 (with a legacy
// %APPDATA%\Roaming\n42 fallback) on Windows and $XDG_DATA_HOME/n42
// or ~/.local/share/n42 on Linux/Unix. homeDir / windowsAppData /
// isNonEmptyDir are the internal helpers driving the selection.

package paths

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const dirname = "n42"

// DefaultDataDir is the default data directory to use for the databases and other
// persistence requirements.
func DefaultDataDir() string {
	// Try to place the data folder in the user's home dir
	home := homeDir()
	if home != "" {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join(home, "Library", dirname)
		case "windows":
			// We used to put everything in %HOME%\AppData\Roaming, but this caused
			// problems with non-typical setups. If this fallback location exists and
			// is non-empty, use it, otherwise DTRT and check %LOCALAPPDATA%.
			fallback := filepath.Join(home, "AppData", "Roaming", dirname)
			appdata := windowsAppData()
			if appdata == "" || isNonEmptyDir(fallback) {
				return fallback
			}
			return filepath.Join(appdata, dirname)
		default:
			if xdgDataDir := os.Getenv("XDG_DATA_HOME"); xdgDataDir != "" {
				return filepath.Join(xdgDataDir, strings.ToLower(dirname))
			}
			return filepath.Join(home, ".local/share", strings.ToLower(dirname))
		}
	}
	// As we cannot guess a stable location, return empty and handle later
	return ""
}

func windowsAppData() string {
	v := os.Getenv("LOCALAPPDATA")
	return v
}

func isNonEmptyDir(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	names, _ := f.Readdir(1)
	f.Close()
	return len(names) > 0
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if usr, err := user.Current(); err == nil {
		return usr.HomeDir
	}
	return ""
}
