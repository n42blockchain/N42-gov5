package leakybucket

import "fmt"

// priorityQueue is a min-heap of LeakyBucket pointers, ordered by their
// drain time (p field). Based on the container/heap package example:
// https://golang.org/pkg/container/heap/
type priorityQueue []*LeakyBucket

func (pq priorityQueue) Len() int {
	return len(pq)
}

// Peek returns the bucket that will drain soonest, or nil if the queue is empty.
func (pq priorityQueue) Peek() *LeakyBucket {
	if len(pq) == 0 {
		return nil
	}
	return pq[0]
}

func (pq priorityQueue) Less(i, j int) bool {
	return pq[i].p.Before(pq[j].p)
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	b, ok := x.(*LeakyBucket)
	if !ok {
		panic(fmt.Sprintf("priorityQueue.Push: expected *LeakyBucket, got %T", x))
	}
	b.index = len(*pq)
	*pq = append(*pq, b)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	b := old[n-1]
	old[n-1] = nil // avoid memory leak
	b.index = -1   // mark as removed
	*pq = old[:n-1]
	return b
}
