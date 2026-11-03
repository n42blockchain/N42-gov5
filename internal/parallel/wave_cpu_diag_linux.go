//go:build linux

package parallel

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// threadCPUNs returns the calling thread's CPU time, 0 on error.
func threadCPUNs() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &ts); err != nil {
		return 0
	}
	return ts.Nano()
}

// currentCPU returns the CPU the calling thread runs on, -1 on error.
func currentCPU() int32 {
	var cpu uint32
	if _, _, errno := unix.RawSyscall(unix.SYS_GETCPU, uintptr(unsafe.Pointer(&cpu)), 0, 0); errno != 0 {
		return -1
	}
	return int32(cpu)
}
