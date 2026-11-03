//go:build !linux

package parallel

// threadCPUNs is unavailable off Linux.
func threadCPUNs() int64 { return 0 }

// currentCPU is unavailable off Linux.
func currentCPU() int32 { return -1 }
