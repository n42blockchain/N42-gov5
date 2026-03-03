package utils

import (
	"context"
	"reflect"
	"runtime"
	"sync"
	"time"

	"github.com/n42blockchain/N42/log"
)

// RunEvery runs the provided command periodically.
// It runs in a goroutine, and can be cancelled by finishing the supplied context.
func RunEvery(ctx context.Context, period time.Duration, f func()) {
	RunEveryWithWG(ctx, period, f, nil)
}

// RunEveryWithWG is like RunEvery but accepts a WaitGroup so the caller can
// wait for the goroutine to exit. The caller must call wg.Add(1) before
// invoking this function if wg is non-nil.
func RunEveryWithWG(ctx context.Context, period time.Duration, f func(), wg *sync.WaitGroup) {
	funcName := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	ticker := time.NewTicker(period)
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		for {
			select {
			case <-ticker.C:
				log.Trace("running", "function", funcName)
				func() {
					defer func() {
						if r := recover(); r != nil {
							buf := make([]byte, 4096)
							n := runtime.Stack(buf, false)
							log.Error("panic in periodic function, recovered", "function", funcName, "panic", r, "stack", string(buf[:n]))
						}
					}()
					f()
				}()
			case <-ctx.Done():
				log.Debug("context is closed, exiting", "function", funcName)
				ticker.Stop()
				return
			}
		}
	}()
}
