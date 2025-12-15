// Copyright 2022 The N42 Authors
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

package pubsub

import (
	"context"
	"errors"
	"google.golang.org/protobuf/proto"
	"sync"
	"sync/atomic"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/message"
	"github.com/n42blockchain/N42/log"
)

var (
	errorInvalidTopic    = errors.New("invalid topic")
	errorNotRunning      = errors.New("n42 pubsub not run")
	errorPubSubIsRunning = errors.New("n42 pubsub is running")
)

type n42PubSub struct {
	topicLock sync.Mutex
	topicsMap map[string]*pubsub.Topic

	p2pserver common.INetwork

	pubsub  *pubsub.PubSub
	running int32

	host host.Host

	ctx context.Context

	chainID uint64
}

func NewPubSub(ctx context.Context, p2pserver common.INetwork, chainid uint64) (common.IPubSub, error) {
	ps := n42PubSub{
		ctx:       ctx,
		host:      p2pserver.Host(),
		p2pserver: p2pserver,
		running:   0,
		topicsMap: make(map[string]*pubsub.Topic),
		chainID:   chainid,
	}

	return &ps, nil
}

func (m *n42PubSub) Start() error {
	if m.isRunning() {
		return errorPubSubIsRunning
	}

	atomic.StoreInt32(&m.running, 1)

	var options []pubsub.Option

	options = append(options, pubsub.WithRawTracer(newRawTracer()) /*, pubsub.WithMessageSignaturePolicy(pubsub.MessageSignaturePolicy(0))*/)
	// todo for test
	if false {
		tracer, err := pubsub.NewJSONTracer("./trace.json")
		if err != nil {
			return err
		}
		options = append(options, pubsub.WithEventTracer(tracer))
	}

	gossip, err := pubsub.NewGossipSub(m.ctx, m.host, options...)
	if err != nil {
		atomic.StoreInt32(&m.running, 0)
		return err
	}

	gossip.GetTopics()

	m.pubsub = gossip

	return nil
}

func (m *n42PubSub) JoinTopic(topic string) (*pubsub.Topic, error) {
	if !m.isRunning() {
		return nil, errorNotRunning
	}
	m.topicLock.Lock()
	defer m.topicLock.Unlock()
	if t, ok := m.topicsMap[topic]; ok {
		return t, nil
	}

	if _, ok := message.TopicMappings[topic]; ok {
		topicHandle, err := m.pubsub.Join(topic)
		if err != nil {
			return nil, err
		}
		m.topicsMap[topic] = topicHandle
		return topicHandle, nil
	}

	return nil, errorInvalidTopic
}

func (m *n42PubSub) isRunning() bool {
	if atomic.LoadInt32(&m.running) <= 0 {
		return false
	}
	return true
}

func (m *n42PubSub) Publish(topic string, msg proto.Message) error {
	if !m.isRunning() {
		return errorNotRunning
	}
	m.topicLock.Lock()
	defer m.topicLock.Unlock()
	if t, ok := m.topicsMap[topic]; ok {
		data, err := proto.Marshal(msg)
		if err != nil {
			log.Errorf("failed to publish topic(%s), data: %s, err: %v", topic, msg, err)
			return err
		}

		return t.Publish(m.ctx, data)
	}

	return errorInvalidTopic
}

func (m *n42PubSub) Subscription(topic string) (*pubsub.Subscription, error) {
	if !m.isRunning() {
		return nil, errorNotRunning
	}
	m.topicLock.Lock()
	defer m.topicLock.Unlock()
	if t, ok := m.topicsMap[topic]; ok {
		return t.Subscribe()
	}

	return nil, errorInvalidTopic
}

func (m *n42PubSub) GetTopics() []string {
	var topics []string
	for k, _ := range m.topicsMap {
		topics = append(topics, k)
	}

	return topics
}
