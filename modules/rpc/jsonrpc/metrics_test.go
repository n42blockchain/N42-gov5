package jsonrpc

import (
	"sync"
	"testing"
)

func TestNewRPCServingTimerMSConcurrentFirstUse(t *testing.T) {
	const workers = 128
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wg.Done()
			<-start
			method := "eth_getBlockByNumber"
			if index%2 == 1 {
				method = "eth_getBlockReceipts"
			}
			_ = newRPCServingTimerMS(method, index%3 != 0)
		}(i)
	}
	close(start)
	wg.Wait()
}
