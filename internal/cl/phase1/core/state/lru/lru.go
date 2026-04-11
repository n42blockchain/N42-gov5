// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Lru unit for the lru package.
// Exports helpers such as Get and Get.
// Part of the n42el consensus-layer build.

//go:build n42el

package lru

import (
	"fmt"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/hashicorp/golang-lru/v2/expirable"

	"github.com/n42blockchain/N42/internal/cl/depshim/metrics"
)

// Cache is a wrapper around hashicorp lru but with metric for Get
type Cache[K comparable, V any] struct {
	*lru.Cache[K, V]
	metricName string
	// metrics
	metricHit, metricMiss metrics.Counter
}

func New[K comparable, V any](metricName string, size int) (*Cache[K, V], error) {
	v, err := lru.NewWithEvict[K, V](size, nil)
	if err != nil {
		return nil, err
	}
	return &Cache[K, V]{
		Cache:      v,
		metricName: metricName,
		metricHit:  metrics.GetOrCreateCounter(fmt.Sprintf(`golang_lru_cache_hit{%s="%s"}`, "cache", metricName)),
		metricMiss: metrics.GetOrCreateCounter(fmt.Sprintf(`golang_lru_cache_miss{%s="%s"}`, "cache", metricName)),
	}, nil
}

func (c *Cache[K, V]) Get(k K) (V, bool) {
	v, ok := c.Cache.Get(k)
	if ok {
		c.metricHit.Inc()
	} else {
		c.metricMiss.Inc()
	}
	return v, ok
}

type CacheWithTTL[K comparable, V any] struct {
	*expirable.LRU[K, V]
	metric string
	// metrics
	metricTTLHit, metricTTLMiss metrics.Counter
}

func NewWithTTL[K comparable, V any](metricName string, size int, ttl time.Duration) *CacheWithTTL[K, V] {
	cache := expirable.NewLRU[K, V](size, nil, ttl)
	return &CacheWithTTL[K, V]{
		LRU:           cache,
		metric:        metricName,
		metricTTLHit:  metrics.GetOrCreateCounter(fmt.Sprintf(`golang_ttl_lru_cache_hit{%s="%s"}`, "cache", metricName)),
		metricTTLMiss: metrics.GetOrCreateCounter(fmt.Sprintf(`golang_ttl_lru_cache_miss{%s="%s"}`, "cache", metricName)),
	}
}

func (c *CacheWithTTL[K, V]) Get(k K) (V, bool) {
	v, ok := c.LRU.Get(k)
	if ok {
		c.metricTTLHit.Inc()
	} else {
		c.metricTTLMiss.Inc()
	}
	return v, ok
}
