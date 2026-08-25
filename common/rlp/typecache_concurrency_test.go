package rlp

import (
	"reflect"
	"sync"
	"testing"
)

type concurrentCacheNode struct {
	Value uint64
	Next  *concurrentCacheNode
}

func TestTypeCacheConcurrentMiss(t *testing.T) {
	const goroutines = 64
	types := make([]reflect.Type, goroutines)
	for i := range types {
		// Each goroutine misses on a distinct type, exercising concurrent map
		// replacement rather than only the already-cached atomic read path.
		types[i] = reflect.ArrayOf(i+1, reflect.TypeOf(byte(0)))
	}

	start := make(chan struct{})
	infos := make([]*typeinfo, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range types {
		go func(i int) {
			defer wg.Done()
			<-start
			infos[i] = cachedTypeInfo(types[i], tags{})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, info := range infos {
		if info == nil || info.writer == nil || info.decoder == nil {
			t.Fatalf("type %v has incomplete cache entry: %#v", types[i], info)
		}
		if again := cachedTypeInfo(types[i], tags{}); again != info {
			t.Fatalf("type %v cache entry was not stable", types[i])
		}
	}

	// Recursive generation must still resolve through the unpublished dummy.
	recursive := cachedTypeInfo(reflect.TypeOf(concurrentCacheNode{}), tags{})
	if recursive.writer == nil || recursive.decoder == nil {
		t.Fatal("recursive type has incomplete cache entry")
	}
}
