// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Event unit for the event package.
// Defines the Subscription, Feed, and feedSub types.
// Exports helpers such as Err, Unsubscribe, Subscribe, and Send.
// Minimal event feed for in-process subscriptions.

//go:build n42el

// Package event is a minimal stub of erigon's p2p/event package. cl/beacon
// uses Feed (typed publish/subscribe) and Subscription (cancel handle).
//
// The implementation distills the standard go-ethereum / erigon Feed
// pattern down to the surface Caplin actually uses (Subscribe, Send,
// Unsubscribe, Err). Slow consumers drop messages — that matches the
// loose-coupling semantics that beacon event listeners assume.
package event

import (
	"reflect"
	"sync"
)

// Subscription is the cancel handle returned by Feed.Subscribe. It is an
// interface to match erigon's p2p/event.Subscription so cl/beacon code that
// stores it in a field of type `Subscription` does not need any rewriting.
type Subscription interface {
	Err() <-chan error
	Unsubscribe()
}

// Feed is a single-typed publish/subscribe primitive.
type Feed struct {
	mu          sync.Mutex
	subscribers []*feedSub
}

type feedSub struct {
	feed *Feed
	ch   reflect.Value
	once sync.Once
	done chan error
}

// Err returns a channel that is closed when the subscription is cancelled.
func (s *feedSub) Err() <-chan error { return s.done }

// Unsubscribe removes the subscription from the feed.
func (s *feedSub) Unsubscribe() {
	s.once.Do(func() {
		close(s.done)
		s.feed.mu.Lock()
		defer s.feed.mu.Unlock()
		for i, sub := range s.feed.subscribers {
			if sub == s {
				s.feed.subscribers = append(s.feed.subscribers[:i], s.feed.subscribers[i+1:]...)
				break
			}
		}
	})
}

// Subscribe attaches the given channel and returns a Subscription handle.
func (f *Feed) Subscribe(channel any) Subscription {
	chv := reflect.ValueOf(channel)
	if chv.Kind() != reflect.Chan {
		panic("event.Feed.Subscribe: argument must be a channel")
	}
	sub := &feedSub{feed: f, ch: chv, done: make(chan error)}
	f.mu.Lock()
	f.subscribers = append(f.subscribers, sub)
	f.mu.Unlock()
	return sub
}

// Send delivers value to every currently-subscribed channel using a
// non-blocking send (slow consumers drop messages). Returns the number of
// successful deliveries.
func (f *Feed) Send(value any) int {
	v := reflect.ValueOf(value)
	f.mu.Lock()
	subs := append([]*feedSub(nil), f.subscribers...)
	f.mu.Unlock()
	delivered := 0
	for _, sub := range subs {
		select {
		case <-sub.done:
			continue
		default:
		}
		if sub.ch.TrySend(v) {
			delivered++
		}
	}
	return delivered
}
