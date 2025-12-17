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

package p2p

import (
	"sync"
)

// MessagePool provides pooled message buffers to reduce allocations.
type MessagePool struct {
	pools []*sync.Pool
}

// Global message pool with different size classes
var messagePool = &MessagePool{
	pools: make([]*sync.Pool, 16), // 256B to 8MB
}

func init() {
	for i := range messagePool.pools {
		size := 256 << uint(i) // Start at 256 bytes
		messagePool.pools[i] = &sync.Pool{
			New: func() interface{} {
				b := make([]byte, size)
				return &b
			},
		}
	}
}

// messageSizeClass returns the pool index for a given size.
func messageSizeClass(size int) int {
	if size <= 256 {
		return 0
	}
	// Find the smallest power of 2 >= size, starting from 256
	class := 0
	s := (size - 1) >> 8 // Divide by 256
	for s > 0 {
		s >>= 1
		class++
	}
	if class >= len(messagePool.pools) {
		return -1 // Too large for pool
	}
	return class
}

// GetMessageBuffer gets a message buffer of at least the given size.
func GetMessageBuffer(size int) []byte {
	class := messageSizeClass(size)
	if class < 0 {
		return make([]byte, size)
	}
	bp := messagePool.pools[class].Get().(*[]byte)
	return (*bp)[:size]
}

// PutMessageBuffer returns a message buffer to the pool.
func PutMessageBuffer(b []byte) {
	class := messageSizeClass(cap(b))
	if class >= 0 && class < len(messagePool.pools) {
		expectedSize := 256 << uint(class)
		if cap(b) == expectedSize {
			bp := b[:cap(b)]
			messagePool.pools[class].Put(&bp)
		}
	}
}

// PeerMessageQueue provides a reusable queue for peer messages.
type PeerMessageQueue struct {
	messages [][]byte
	mu       sync.Mutex
	maxSize  int
}

// NewPeerMessageQueue creates a new peer message queue.
func NewPeerMessageQueue(maxSize int) *PeerMessageQueue {
	return &PeerMessageQueue{
		messages: make([][]byte, 0, maxSize),
		maxSize:  maxSize,
	}
}

// Enqueue adds a message to the queue.
func (q *PeerMessageQueue) Enqueue(msg []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.messages) >= q.maxSize {
		return false // Queue full
	}
	q.messages = append(q.messages, msg)
	return true
}

// Dequeue removes and returns the oldest message from the queue.
func (q *PeerMessageQueue) Dequeue() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.messages) == 0 {
		return nil
	}
	msg := q.messages[0]
	q.messages = q.messages[1:]
	return msg
}

// Len returns the current queue length.
func (q *PeerMessageQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.messages)
}

// Clear empties the queue and returns all messages for recycling.
func (q *PeerMessageQueue) Clear() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()

	msgs := q.messages
	q.messages = q.messages[:0]
	return msgs
}

// BatchSend groups messages for efficient batch transmission.
type BatchSend struct {
	messages [][]byte
	totalLen int
	maxBatch int
	mu       sync.Mutex
}

// NewBatchSend creates a new batch sender.
func NewBatchSend(maxBatch int) *BatchSend {
	return &BatchSend{
		messages: make([][]byte, 0, maxBatch),
		maxBatch: maxBatch,
	}
}

// Add adds a message to the batch.
func (b *BatchSend) Add(msg []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.messages) >= b.maxBatch {
		return false
	}
	b.messages = append(b.messages, msg)
	b.totalLen += len(msg)
	return true
}

// Flush returns all messages and resets the batch.
func (b *BatchSend) Flush() ([][]byte, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	msgs := b.messages
	totalLen := b.totalLen
	b.messages = make([][]byte, 0, b.maxBatch)
	b.totalLen = 0
	return msgs, totalLen
}

// Len returns the current batch size.
func (b *BatchSend) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.messages)
}

// TotalLen returns the total bytes in the batch.
func (b *BatchSend) TotalLen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalLen
}

