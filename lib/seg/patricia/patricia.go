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

package patricia

import (
	"fmt"
	"math/bits"
	"slices"

	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/lib/seg/sais"
)

type MatchFinder struct {
	pt      *PatriciaTree
	s       pathWalker
	matches []Match
}

func NewMatchFinder(pt *PatriciaTree) *MatchFinder {
	return &MatchFinder{pt: pt}
}

type MatchFinder2 struct {
	top        *node // Top of nodeStack
	pt         *PatriciaTree
	nodeStack  []*node
	matchStack []Match
	matches    Matches
	sa         []int32
	lcp        []int32
	inv        []int32
	headLen    int
	tailLen    int
	side       int // 0, 1, or 2 (if side is not determined yet)
}

func NewMatchFinder2(pt *PatriciaTree) *MatchFinder2 {
	return &MatchFinder2{pt: pt, top: &pt.root, nodeStack: []*node{&pt.root}, side: 2}
}

// unfold consumes next byte of the key, moves the MatchFinder2 to corresponding
// node of the patricia tree and returns divergence prefix (0 if there is no divergence)
func (mf2 *MatchFinder2) unfold(b byte) uint32 {
	bitsLeft := 8 // Bits in b to process
	b32 := uint32(b) << 24
	for bitsLeft > 0 {
		if mf2.side == 2 {
			// tail has not been determined yet, do it now
			if b32&0x80000000 == 0 {
				mf2.side = 0
				mf2.headLen = 0
				mf2.tailLen = int(mf2.top.p0 & 0x1f)
			} else {
				mf2.side = 1
				mf2.headLen = 0
				mf2.tailLen = int(mf2.top.p1 & 0x1f)
			}
			if mf2.tailLen == 0 {
				// positioned at the end of the current node
				mf2.side = 2
				return b32 | uint32(bitsLeft)
			}
		}
		if mf2.tailLen == 0 {
			// Need to switch to the next node
			if mf2.side == 0 {
				if mf2.top.n0 == nil {
					return b32 | uint32(bitsLeft)
				}
				mf2.nodeStack = append(mf2.nodeStack, mf2.top.n0)
				mf2.top = mf2.top.n0
			} else if mf2.side == 1 {
				if mf2.top.n1 == nil {
					return b32 | uint32(bitsLeft)
				}
				mf2.nodeStack = append(mf2.nodeStack, mf2.top.n1)
				mf2.top = mf2.top.n1
			} else {
				panic("")
			}
			mf2.headLen = 0
			mf2.side = 2
		}
		var tail uint32
		if mf2.side == 0 {
			tail = (mf2.top.p0 & 0xffffffe0) << mf2.headLen
		} else if mf2.side == 1 {
			tail = (mf2.top.p1 & 0xffffffe0) << mf2.headLen
		} else {
			return b32 | uint32(bitsLeft)
		}
		firstDiff := bits.LeadingZeros32(tail ^ b32) // First bit where b32 and tail are different
		if firstDiff < bitsLeft {
			// divergence is within currently supplied byte of the search key, b
			if firstDiff >= mf2.tailLen {
				// divergence is outside of the current node
				bitsLeft -= mf2.tailLen
				b32 <<= mf2.tailLen
				mf2.headLen += mf2.tailLen
				mf2.tailLen = 0
			} else {
				// divergence is within the current node
				bitsLeft -= firstDiff
				b32 <<= firstDiff
				mf2.tailLen -= firstDiff
				mf2.headLen += firstDiff
				return b32 | uint32(bitsLeft)
			}
		} else if mf2.tailLen < bitsLeft {
			// divergence is outside of currently supplied byte of the search key, b
			bitsLeft -= mf2.tailLen
			b32 <<= mf2.tailLen
			mf2.headLen += mf2.tailLen
			mf2.tailLen = 0
		} else {
			// key byte is consumed, but stay on the same node
			mf2.tailLen -= bitsLeft
			mf2.headLen += bitsLeft
			bitsLeft = 0
			b32 = 0
		}
		if mf2.tailLen == 0 {
			// Need to switch to the next node
			if mf2.side == 0 {
				if mf2.top.n0 == nil {
					return b32 | uint32(bitsLeft)
				}
				mf2.nodeStack = append(mf2.nodeStack, mf2.top.n0)
				mf2.top = mf2.top.n0
			} else if mf2.side == 1 {
				if mf2.top.n1 == nil {
					return b32 | uint32(bitsLeft)
				}
				mf2.nodeStack = append(mf2.nodeStack, mf2.top.n1)
				mf2.top = mf2.top.n1
			} else {
				panic("")
			}
			mf2.headLen = 0
			mf2.side = 2
		}
	}
	return 0
}

// fold moves the match finder back up the stack by specified number of bits
func (mf2 *MatchFinder2) fold(bits int) {
	bitsLeft := bits
	for bitsLeft > 0 {
		if mf2.headLen == bitsLeft {
			mf2.headLen = 0
			mf2.tailLen = 0
			mf2.side = 2
			bitsLeft = 0
		} else if mf2.headLen >= bitsLeft {
			// folding only affects top node
			mf2.headLen -= bitsLeft
			mf2.tailLen += bitsLeft
			bitsLeft = 0
		} else {
			// folding affects not only top node, remove top node
			bitsLeft -= mf2.headLen
			mf2.nodeStack = mf2.nodeStack[:len(mf2.nodeStack)-1]
			prevTop := mf2.top
			mf2.top = mf2.nodeStack[len(mf2.nodeStack)-1]
			if mf2.top.n0 == prevTop {
				mf2.side = 0
				mf2.headLen = int(mf2.top.p0 & 0x1f)
			} else if mf2.top.n1 == prevTop {
				mf2.side = 1
				mf2.headLen = int(mf2.top.p1 & 0x1f)
			} else {
				panic("")
			}
			mf2.tailLen = 0
		}
	}
}

func (mf2 *MatchFinder2) FindLongestMatches(data []byte) []Match {
	mf2.matches = mf2.matches[:0]
	if len(data) < 2 {
		return mf2.matches
	}
	mf2.nodeStack = append(mf2.nodeStack[:0], &mf2.pt.root)
	mf2.matchStack = mf2.matchStack[:0]
	mf2.top = &mf2.pt.root
	mf2.side = 2
	mf2.tailLen = 0
	mf2.headLen = 0
	n := len(data)
	if cap(mf2.sa) < n {
		mf2.sa = make([]int32, n)
	} else {
		mf2.sa = mf2.sa[:n]
	}
	if err := sais.Sais(data, mf2.sa); err != nil {
		panic(err)
	}
	if cap(mf2.inv) < n {
		mf2.inv = make([]int32, n)
	} else {
		mf2.inv = mf2.inv[:n]
	}
	for i := 0; i < n; i++ {
		mf2.inv[mf2.sa[i]] = int32(i)
	}
	var k int
	// Process all suffixes one by one starting from first suffix in txt[]
	if cap(mf2.lcp) < n {
		mf2.lcp = make([]int32, n)
	} else {
		mf2.lcp = mf2.lcp[:n]
	}
	for i := 0; i < n; i++ {
		if mf2.inv[i] == int32(n-1) {
			k = 0
			continue
		}
		j := int(mf2.sa[mf2.inv[i]+1])
		for i+k < n && j+k < n && data[i+k] == data[j+k] {
			k++
		}
		mf2.lcp[mf2.inv[i]] = int32(k)
		if k > 0 {
			k--
		}
	}
	depth := 0 // Depth in bits
	var lastMatch *Match
	for i := 0; i < n; i++ {
		if i > 0 {
			lcp := int(mf2.lcp[i-1])
			if depth > 8*lcp {
				mf2.fold(depth - 8*lcp)
				depth = 8 * lcp
				for lastMatch != nil && lastMatch.End-lastMatch.Start > lcp {
					mf2.matchStack = mf2.matchStack[:len(mf2.matchStack)-1]
					if len(mf2.matchStack) == 0 {
						lastMatch = nil
					} else {
						lastMatch = &mf2.matchStack[len(mf2.matchStack)-1]
					}
				}
			} else {
				r := depth % 8
				if r > 0 {
					mf2.fold(r)
					depth -= r
				}
			}
		}
		sa := int(mf2.sa[i])
		start := sa + depth/8
		for end := start + 1; end <= n; end++ {
			d := mf2.unfold(data[end-1])
			depth += 8 - int(d&0x1f)
			if d != 0 {
				break
			}
			if mf2.tailLen != 0 || mf2.top.val == nil {
				continue
			}
			if cap(mf2.matchStack) == len(mf2.matchStack) {
				mf2.matchStack = append(mf2.matchStack, Match{})
			} else {
				mf2.matchStack = mf2.matchStack[:len(mf2.matchStack)+1]
			}
			lastMatch = &mf2.matchStack[len(mf2.matchStack)-1]
			lastMatch.Start = sa
			lastMatch.End = end
			lastMatch.Val = mf2.top.val
		}
		if lastMatch != nil {
			mf2.matches = append(mf2.matches, Match{})
			m := &mf2.matches[len(mf2.matches)-1]
			m.Start = sa
			m.End = sa + lastMatch.End - lastMatch.Start
			m.Val = lastMatch.Val
		}
	}
	if len(mf2.matches) < 2 {
		return mf2.matches
	}
	slices.SortFunc(mf2.matches, func(i, j Match) int { return cmp.Compare(i.Start, j.Start) })

	lastEnd := mf2.matches[0].End
	j := 1
	for i, m := range mf2.matches {
		if i > 0 {
			if m.End > lastEnd {
				if i != j {
					mf2.matches[j] = m
				}
				lastEnd = m.End
				j++
			}
		}
	}
	return mf2.matches[:j]
}

func (mf2 *MatchFinder2) Current() ([]byte, int) {
	var b []byte
	var depth int
	last := len(mf2.nodeStack) - 1
	for i, n := range mf2.nodeStack {
		var p uint32
		if i < last {
			next := mf2.nodeStack[i+1]
			if n.n0 == next {
				p = n.p0
			} else if n.n1 == next {
				p = n.p1
			} else {
				panic("")
			}
		} else {
			if mf2.side == 0 {
				p = n.p0
			} else if mf2.side == 1 {
				p = n.p1
			}
			p = (p & 0xffffffe0) | uint32(mf2.headLen)
		}
		fmt.Printf("i,p=%d, %b\n", i, p)
		// Add bit by bit
		for (p & 0x1f) > 0 {
			if depth >= 8*len(b) {
				b = append(b, 0)
			}
			if p&0x80000000 != 0 {
				b[depth/8] |= uint8(1) << (7 - (depth % 8))
			}
			depth++
			p = ((p & 0xffffffe0) << 1) | (p & 0x1f) - 1
		}
	}
	return b, depth
}

func (mf *MatchFinder) FindLongestMatches(data []byte) []Match {
	matchCount := 0
	s := &mf.s
	lastEnd := 0
	for start := 0; start < len(data); start++ {
		s.reset(&mf.pt.root)
		emitted := false
		for end := start + 1; end <= len(data); end++ {
			if d := s.transition(data[end-1], true /* readonly */); d != 0 {
				break
			}
			if s.tail != 0 || s.n.val == nil || end <= lastEnd {
				continue
			}
			var m *Match
			if emitted {
				m = &mf.matches[matchCount-1]
			} else {
				if matchCount == len(mf.matches) {
					mf.matches = append(mf.matches, Match{})
					m = &mf.matches[len(mf.matches)-1]
				} else {
					m = &mf.matches[matchCount]
				}
				matchCount++
				emitted = true
			}
			// This possibly overwrites previous match for the same start position
			m.Start = start
			m.End = end
			m.Val = s.n.val
			lastEnd = end
		}
	}
	return mf.matches[:matchCount]
}
