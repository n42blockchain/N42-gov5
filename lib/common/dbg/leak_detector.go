package dbg

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/lib/log/v3"
)

const FileCloseLogLevel = log.LvlTrace

// LeakDetector detects resources that were created but not closed (leaked).
// It periodically logs resources that have been alive longer than a threshold, with their creation stack trace.
// For example, db transactions can call Add/Del from Begin/Commit/Rollback methods.
type LeakDetector struct {
	enabled       atomic.Bool
	slowThreshold atomic.Pointer[time.Duration]
	autoIncrement atomic.Uint64

	list     map[uint64]LeakDetectorItem
	listLock sync.Mutex
}

type LeakDetectorItem struct {
	stack   string
	started time.Time
}

func NewLeakDetector(name string, slowThreshold time.Duration) *LeakDetector {
	if slowThreshold <= 0 {
		return nil
	}
	d := &LeakDetector{list: map[uint64]LeakDetectorItem{}}
	d.SetSlowThreshold(slowThreshold)

	go func() {
		logEvery := time.NewTicker(60 * time.Second)
		defer logEvery.Stop()

		for range logEvery.C {
			if list := d.slowList(); len(list) > 0 {
				log.Info(fmt.Sprintf("[dbg.%s] long living resources", name), "list", strings.Join(list, ", "))
			}
		}
	}()

	return d
}

// maxScanEntries limits map iteration to protect logs from excessive output.
const maxScanEntries = 10

func (d *LeakDetector) slowList() []string {
	if d == nil || !d.Enabled() {
		return nil
	}
	threshold := *d.slowThreshold.Load()

	d.listLock.Lock()
	defer d.listLock.Unlock()

	var (
		res     []string
		scanned int
	)
	for key, item := range d.list {
		scanned++
		if scanned > maxScanEntries {
			break
		}
		if age := time.Since(item.started); age > threshold {
			res = append(res, fmt.Sprintf("%d(%s): %s", key, age, item.stack))
		}
	}
	return res
}

// Del removes a tracked resource by its ID.
func (d *LeakDetector) Del(id uint64) {
	if d == nil || !d.Enabled() {
		return
	}
	d.listLock.Lock()
	defer d.listLock.Unlock()
	delete(d.list, id)
}

// Add registers a new resource for leak tracking and returns its ID.
func (d *LeakDetector) Add() uint64 {
	if d == nil || !d.Enabled() {
		return 0
	}
	ac := LeakDetectorItem{
		stack:   StackSkip(2),
		started: time.Now(),
	}
	id := d.autoIncrement.Add(1)
	d.listLock.Lock()
	defer d.listLock.Unlock()
	d.list[id] = ac
	return id
}

func (d *LeakDetector) Enabled() bool { return d.enabled.Load() }
func (d *LeakDetector) SetSlowThreshold(t time.Duration) {
	d.slowThreshold.Store(&t)
	d.enabled.Store(t > 0)
}
