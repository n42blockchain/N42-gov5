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
	"strings"
)

// Implementation of patricia tree for efficient search of substrings from a dictionary in a given string

type node struct {
	val interface{} // value associated with the key
	n0  *node
	n1  *node
	p0  uint32
	p1  uint32
}

func tostr(x uint32) string {
	str := fmt.Sprintf("%b", x)
	for len(str) < 32 {
		str = "0" + str
	}
	return str[:x&0x1f]
}

// print assumes values are byte slices
func (n *node) print(sb *strings.Builder, indent string) {
	sb.WriteString(indent)
	fmt.Fprintf(sb, "%p ", n)
	sb.WriteString(tostr(n.p0))
	sb.WriteString("\n")
	if n.n0 != nil {
		n.n0.print(sb, indent+"    ")
	}
	sb.WriteString(indent)
	fmt.Fprintf(sb, "%p ", n)
	sb.WriteString(tostr(n.p1))
	sb.WriteString("\n")
	if n.n1 != nil {
		n.n1.print(sb, indent+"    ")
	}
	if n.val != nil {
		sb.WriteString(indent)
		sb.WriteString("val:")
		fmt.Fprintf(sb, " %x", n.val.([]byte))
		sb.WriteString("\n")
	}
}

func (n *node) String() string {
	var sb strings.Builder
	n.print(&sb, "")
	return sb.String()
}

// pathWalker represents a position anywhere inside patricia tree
// position can be identified by combination of node, and the partitioning
// of that node's p0 or p1 into head and tail.
// As with p0 and p1, head and tail are encoded as follows:
// lowest 5 bits encode the length in bits, and the remaining 27 bits
// encode the actual head or tail.
// For example, if the position is at the beginning of a node,
// head would be zero, and tail would be equal to either p0 or p1,
// depending on whether the position corresponds to going left (0) or right (1).
type pathWalker struct {
	n    *node
	head uint32
	tail uint32
}

func (s *pathWalker) String() string {
	return fmt.Sprintf("%p head %s tail %s", s.n, tostr(s.head), tostr(s.tail))
}

func (s *pathWalker) reset(n *node) {
	s.n = n
	s.head = 0
	s.tail = 0
}

func newPathWalker(n *node) *pathWalker {
	return &pathWalker{n: n, head: 0, tail: 0}
}

// transition consumes next byte of the key, moves the path walker to corresponding
// node of the patricia tree and returns divergence prefix (0 if there is no divergence)
func (s *pathWalker) transition(b byte, readonly bool) uint32 {
	bitsLeft := 8 // Bits in b to process
	b32 := uint32(b) << 24
	for bitsLeft > 0 {
		if s.head == 0 {
			// tail has not been determined yet, do it now
			if b32&0x80000000 == 0 {
				s.tail = s.n.p0
			} else {
				s.tail = s.n.p1
			}
		}
		if s.tail == 0 {
			// positioned at the end of the current node
			return b32 | uint32(bitsLeft)
		}
		tailLen := int(s.tail & 0x1f)
		firstDiff := bits.LeadingZeros32(s.tail ^ b32) // First bit where b32 and tail are different
		if firstDiff < bitsLeft {
			// divergence is within currently supplied byte of the search key, b
			if firstDiff >= tailLen {
				// divergence is outside of the current node
				bitsLeft -= tailLen
				b32 <<= tailLen
				// Need to switch to the next node
				if (s.head == 0 && s.tail&0x80000000 == 0) || (s.head != 0 && s.head&0x80000000 == 0) {
					if s.n.n0 == nil {
						panic("")
					}
					s.n = s.n.n0
				} else {
					if s.n.n1 == nil {
						panic("")
					}
					s.n = s.n.n1
				}
				s.head = 0
				s.tail = 0
			} else {
				// divergence is within the current node
				bitsLeft -= firstDiff
				b32 <<= firstDiff
				// there is divergence, move head and tail
				mask := ^(uint32(1)<<(32-firstDiff) - 1)
				s.head |= (s.tail & mask) >> (s.head & 0x1f)
				s.head += uint32(firstDiff)
				s.tail = (s.tail&0xffffffe0)<<firstDiff | (s.tail & 0x1f)
				s.tail -= uint32(firstDiff)
				return b32 | uint32(bitsLeft)
			}
		} else if tailLen < bitsLeft {
			// divergence is outside of currently supplied byte of the search key, b
			bitsLeft -= tailLen
			b32 <<= tailLen
			// Switch to the next node
			if (s.head == 0 && s.tail&0x80000000 == 0) || (s.head != 0 && s.head&0x80000000 == 0) {
				if s.n.n0 == nil {
					if readonly {
						return b32 | uint32(bitsLeft)
					}
					s.n.n0 = &node{}
					if b32&0x80000000 == 0 {
						s.n.n0.p0 = b32 | uint32(bitsLeft)
					} else {
						s.n.n0.p1 = b32 | uint32(bitsLeft)
					}
				}
				s.n = s.n.n0
			} else {
				if s.n.n1 == nil {
					if readonly {
						return b32 | uint32(bitsLeft)
					}
					s.n.n1 = &node{}
					if b32&0x80000000 == 0 {
						s.n.n1.p0 = b32 | uint32(bitsLeft)
					} else {
						s.n.n1.p1 = b32 | uint32(bitsLeft)
					}
				}
				s.n = s.n.n1
			}
			s.head = 0
			s.tail = 0
		} else {
			// key byte is consumed, but stay on the same node
			mask := ^(uint32(1)<<(32-bitsLeft) - 1)
			s.head |= (s.tail & mask) >> (s.head & 0x1f)
			s.head += uint32(bitsLeft)
			s.tail = (s.tail&0xffffffe0)<<bitsLeft | (s.tail & 0x1f)
			s.tail -= uint32(bitsLeft)
			bitsLeft = 0
			if s.tail == 0 {
				if s.head&0x80000000 == 0 {
					if s.n.n0 != nil {
						s.n = s.n.n0
						s.head = 0
					}
				} else {
					if s.n.n1 != nil {
						s.n = s.n.n1
						s.head = 0
					}
				}
			}
		}
	}
	return 0
}

func (s *pathWalker) diverge(divergence uint32) {
	if s.tail == 0 {
		// try to add to the existing head
		dLen := int(divergence & 0x1f)
		headLen := int(s.head & 0x1f)
		d32 := divergence & 0xffffffe0
		if headLen+dLen > 27 {
			mask := ^(uint32(1)<<(headLen+5) - 1)
			s.head |= (d32 & mask) >> headLen
			s.head += uint32(27 - headLen)
			var dn node
			if (s.head == 0 && s.tail&0x80000000 == 0) || (s.head != 0 && s.head&0x80000000 == 0) {
				s.n.p0 = s.head
				s.n.n0 = &dn
			} else {
				s.n.p1 = s.head
				s.n.n1 = &dn
			}
			s.n = &dn
			s.head = 0
			s.tail = 0
			d32 <<= 27 - headLen
			dLen -= (27 - headLen)
			headLen = 0
		}
		mask := ^(uint32(1)<<(32-dLen) - 1)
		s.head |= (d32 & mask) >> headLen
		s.head += uint32(dLen)
		if (s.head == 0 && s.tail&0x80000000 == 0) || (s.head != 0 && s.head&0x80000000 == 0) {
			s.n.p0 = s.head
		} else {
			s.n.p1 = s.head
		}
		return
	}
	// create a new node
	var dn node
	if divergence&0x80000000 == 0 {
		dn.p0 = divergence
		dn.p1 = s.tail
		if (s.head == 0 && s.tail&0x80000000 == 0) || (s.head != 0 && s.head&0x80000000 == 0) {
			dn.n1 = s.n.n0
		} else {
			dn.n1 = s.n.n1
		}
	} else {
		dn.p1 = divergence
		dn.p0 = s.tail
		if (s.head == 0 && s.tail&0x80000000 == 0) || (s.head != 0 && s.head&0x80000000 == 0) {
			dn.n0 = s.n.n0
		} else {
			dn.n0 = s.n.n1
		}
	}
	if (s.head == 0 && s.tail&0x80000000 == 0) || (s.head != 0 && s.head&0x80000000 == 0) {
		s.n.n0 = &dn
		s.n.p0 = s.head
	} else {
		s.n.n1 = &dn
		s.n.p1 = s.head
	}
	s.n = &dn
	s.head = divergence
	s.tail = 0
}

func (n *node) insert(key []byte, value interface{}) {
	s := newPathWalker(n)
	for _, b := range key {
		divergence := s.transition(b, false /* readonly */)
		if divergence != 0 {
			s.diverge(divergence)
		}
	}
	s.insert(value)
}

func (s *pathWalker) insert(value interface{}) {
	if s.tail != 0 {
		s.diverge(0)
	}
	if s.head != 0 {
		var dn node
		if s.head&0x80000000 == 0 {
			s.n.n0 = &dn
		} else {
			s.n.n1 = &dn
		}
		s.n = &dn
		s.head = 0
	}
	s.n.val = value
}

func (n *node) get(key []byte) (interface{}, bool) {
	s := newPathWalker(n)
	for _, b := range key {
		divergence := s.transition(b, true /* readonly */)
		if divergence != 0 {
			return nil, false
		}
	}
	if s.tail != 0 {
		return nil, false
	}
	return s.n.val, s.n.val != nil
}

type PatriciaTree struct {
	root node
}

func (pt *PatriciaTree) Insert(key []byte, value interface{}) {
	pt.root.insert(key, value)
}

func (pt *PatriciaTree) Get(key []byte) (interface{}, bool) {
	return pt.root.get(key)
}

type Match struct {
	Val   interface{}
	Start int
	End   int
}

type Matches []Match

func (m Matches) Len() int {
	return len(m)
}

func (m Matches) Less(i, j int) bool {
	return m[i].Start < m[j].Start
}

func (m *Matches) Swap(i, j int) {
	(*m)[i], (*m)[j] = (*m)[j], (*m)[i]
}
