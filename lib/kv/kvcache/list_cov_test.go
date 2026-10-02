package kvcache

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListCovBasicOps exercises the doubly linked List helper methods that
// have no coverage: Front/Back/Next/Prev/PushBack/InsertBefore/InsertAfter/
// MoveToBack/MoveBefore/MoveAfter.
func TestListCovBasicOps(t *testing.T) {
	require := require.New(t)
	l := NewList()

	require.Nil(l.Front())
	require.Nil(l.Back())

	e1 := &Element{K: []byte{1}}
	e2 := &Element{K: []byte{2}}
	e3 := &Element{K: []byte{3}}

	l.PushBack(e1)
	require.Equal(e1, l.Front())
	require.Equal(e1, l.Back())
	require.Nil(e1.Next())
	require.Nil(e1.Prev())

	l.PushBack(e2)
	require.Equal(e1, l.Front())
	require.Equal(e2, l.Back())
	require.Equal(e2, e1.Next())
	require.Equal(e1, e2.Prev())

	// InsertBefore e2 -> e3
	l.InsertBefore(e3, e2)
	require.Equal(3, l.Len())
	require.Equal(e3, e1.Next())
	require.Equal(e2, e3.Next())

	// InsertAfter with a mark not in the list returns nil and does not modify
	foreign := &Element{K: []byte{9}}
	notInList := &Element{K: []byte{10}}
	got := l.InsertAfter(foreign, notInList)
	require.Nil(got)
	require.Equal(3, l.Len())

	// InsertBefore with a mark not in the list returns nil
	got = l.InsertBefore(foreign, notInList)
	require.Nil(got)

	// InsertAfter e1 -> e4
	e4 := &Element{K: []byte{4}}
	l.InsertAfter(e4, e1)
	require.Equal(4, l.Len())
	require.Equal(e4, e1.Next())
	require.Equal(e1, e4.Prev())

	// MoveToBack
	l.MoveToBack(e1)
	require.Equal(e1, l.Back())
	// moving the element already at back is a no-op path
	l.MoveToBack(e1)
	require.Equal(e1, l.Back())

	// MoveToBack / MoveToFront with foreign element (not in list) is a no-op
	l.MoveToBack(foreign)
	l.MoveToFront(foreign)

	// MoveBefore / MoveAfter
	front := l.Front()
	back := l.Back()
	l.MoveBefore(back, front)
	require.Equal(back, l.Front())

	// MoveBefore no-op cases: e == mark, or either not in list
	l.MoveBefore(front, front)
	l.MoveBefore(foreign, front)
	l.MoveBefore(front, foreign)

	l.MoveAfter(front, back)
	require.Equal(back, l.Front())
	// MoveAfter no-op cases
	l.MoveAfter(front, front)
	l.MoveAfter(foreign, front)
	l.MoveAfter(front, foreign)

	// Remove via public Remove returns K,V
	k, v := l.Remove(front)
	require.Equal(front.K, k)
	_ = v

	// Remove on an element not part of any list is a no-op
	l.Remove(foreign)
}

func TestListCovMoveSelf(t *testing.T) {
	l := NewList()
	e1 := &Element{K: []byte{1}}
	l.PushFront(e1)
	// move(e, at) with e == at returns immediately
	l.MoveToFront(e1) // e.list.root.next == e already -> no-op branch
}

// TestThreadSafeEvictionListOldestEmpty covers Oldest() on an empty list.
func TestThreadSafeEvictionListOldestEmpty(t *testing.T) {
	require := require.New(t)
	tsl := &ThreadSafeEvictionList{l: NewList()}
	require.Nil(tsl.Oldest())
	require.Equal(0, tsl.Len())
	require.Equal(0, tsl.Size())
}
