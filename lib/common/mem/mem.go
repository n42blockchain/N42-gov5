//go:build !linux

package mem

import "github.com/shirou/gopsutil/v4/process"

func ReadVirtualMemStats() (process.MemoryMapsStat, error) {
	return process.MemoryMapsStat{}, ErrorUnsupportedPlatform
}

func UpdatePrometheusVirtualMemStats(p process.MemoryMapsStat) {}
