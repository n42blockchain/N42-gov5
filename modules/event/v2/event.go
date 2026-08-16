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
// Global type-keyed event hub built atop Feed/SubscriptionScope.
// GlobalEvent demultiplexes Subscribe/Send calls by reflect type name,
// lazily creating a Feed and SubscriptionScope per channel element type.
// Guarantees a single init via sync.Once and protects the feeds map with
// an RWMutex so concurrent Subscribe and Send share the same scope.

package v2

import (
	"github.com/n42blockchain/N42/log"
	"reflect"
	"sync"
)

var GlobalEvent Event

type Event struct {
	once sync.Once

	feeds      map[string]*Feed
	feedsLock  sync.RWMutex
	feedsScope map[string]*SubscriptionScope
}

func (e *Event) init() {
	e.feeds = make(map[string]*Feed)
	e.feedsScope = make(map[string]*SubscriptionScope)

}

func (e *Event) initKey(key string) {
	e.feedsLock.Lock()
	defer e.feedsLock.Unlock()
	if _, ok := e.feeds[key]; !ok {
		e.feeds[key] = new(Feed)
		e.feedsScope[key] = new(SubscriptionScope)
	}
}

func (e *Event) Subscribe(channel interface{}) (Subscription, error) {
	e.once.Do(e.init)

	key := reflect.TypeOf(channel).Elem().String()
	e.initKey(key)

	e.feedsLock.RLock()
	defer e.feedsLock.RUnlock()
	sub, err := e.feeds[key].Subscribe(channel)
	if err != nil {
		return nil, err
	}
	return e.feedsScope[key].Track(sub), nil
}

func (e *Event) Send(value interface{}) int {

	e.once.Do(e.init)

	key := reflect.TypeOf(value).String()
	e.initKey(key)

	nsent := 0

	e.feedsLock.RLock()
	defer e.feedsLock.RUnlock()

	log.Trace("GlobalEvent Send", "key", key)
	if e.feedsScope[key].Count() == 0 {
		return nsent
	}
	nsent = e.feeds[key].Send(value)

	return nsent
}

// HasSubscribers reports whether any channel is currently subscribed for
// values of the sample's dynamic type. Send already drops a value nobody
// listens for, but only AFTER the caller built it — this lets a producer skip
// building an expensive payload at all (the mined-block Entire event marshals
// every transaction of a full block; ~23k encodings a block for, usually, no
// subscriber).
func (e *Event) HasSubscribers(sample interface{}) bool {
	e.once.Do(e.init)

	key := reflect.TypeOf(sample).String()

	e.feedsLock.RLock()
	defer e.feedsLock.RUnlock()
	scope, ok := e.feedsScope[key]
	return ok && scope.Count() > 0
}

func (e *Event) Close() {
	e.feedsLock.Lock()
	defer e.feedsLock.Unlock()

	for _, scope := range e.feedsScope {
		scope.Close()
	}

	// Reset state so the Event can be re-initialized on next use.
	e.feeds = nil
	e.feedsScope = nil
	e.once = sync.Once{}
}
