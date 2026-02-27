/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package direct

import (
	"context"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/lib/gointerfaces/sentry"
)

// -- Messages stream types

type inboundMessageReply struct {
	r   *sentry.InboundMessage
	err error
}

// SentryMessagesStreamS implements sentry.Sentry_MessagesServer.
type SentryMessagesStreamS struct {
	ch  chan *inboundMessageReply
	ctx context.Context
	grpc.ServerStream
}

func (s *SentryMessagesStreamS) Send(m *sentry.InboundMessage) error {
	s.ch <- &inboundMessageReply{r: m}
	return nil
}

func (s *SentryMessagesStreamS) Context() context.Context { return s.ctx }

func (s *SentryMessagesStreamS) Err(err error) {
	if err == nil {
		return
	}
	s.ch <- &inboundMessageReply{err: err}
}

// SentryMessagesStreamC implements sentry.Sentry_MessagesClient.
type SentryMessagesStreamC struct {
	ch  chan *inboundMessageReply
	ctx context.Context
	grpc.ClientStream
}

func (c *SentryMessagesStreamC) Recv() (*sentry.InboundMessage, error) {
	m, ok := <-c.ch
	if !ok || m == nil {
		return nil, io.EOF
	}
	return m.r, m.err
}

func (c *SentryMessagesStreamC) Context() context.Context { return c.ctx }

func (c *SentryMessagesStreamC) RecvMsg(anyMessage interface{}) error {
	m, err := c.Recv()
	if err != nil {
		return err
	}
	outMessage := anyMessage.(*sentry.InboundMessage)
	proto.Merge(outMessage, m)
	return nil
}

// -- PeerEvents stream types

type peersReply struct {
	r   *sentry.PeerEvent
	err error
}

// SentryPeersStreamS implements sentry.Sentry_PeerEventsServer.
type SentryPeersStreamS struct {
	ch  chan *peersReply
	ctx context.Context
	grpc.ServerStream
}

func (s *SentryPeersStreamS) Send(m *sentry.PeerEvent) error {
	s.ch <- &peersReply{r: m}
	return nil
}

func (s *SentryPeersStreamS) Context() context.Context { return s.ctx }

func (s *SentryPeersStreamS) Err(err error) {
	if err == nil {
		return
	}
	s.ch <- &peersReply{err: err}
}

// SentryPeersStreamC implements sentry.Sentry_PeerEventsClient.
type SentryPeersStreamC struct {
	ch  chan *peersReply
	ctx context.Context
	grpc.ClientStream
}

func (c *SentryPeersStreamC) Recv() (*sentry.PeerEvent, error) {
	m, ok := <-c.ch
	if !ok || m == nil {
		return nil, io.EOF
	}
	return m.r, m.err
}

func (c *SentryPeersStreamC) Context() context.Context { return c.ctx }

func (c *SentryPeersStreamC) RecvMsg(anyMessage interface{}) error {
	m, err := c.Recv()
	if err != nil {
		return err
	}
	outMessage := anyMessage.(*sentry.PeerEvent)
	proto.Merge(outMessage, m)
	return nil
}
