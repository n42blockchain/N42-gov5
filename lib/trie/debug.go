// Copyright 2019 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package trie

import (
	"fmt"
	"io"
)

func (n *fullNode) fstring(ind string) string {
	resp := fmt.Sprintf("full\n%s  ", ind)
	for i, node := range &n.Children {
		if node == nil {
			resp += fmt.Sprintf("%s: <nil> ", indices[i])
		} else {
			resp += fmt.Sprintf("%s: %v", indices[i], node.fstring(ind+"  "))
		}
	}
	return resp + fmt.Sprintf("\n%s] ", ind)
}
func (n *fullNode) print(w io.Writer) {
	fmt.Fprintf(w, "f(")
	for i, node := range &n.Children {
		if node != nil {
			fmt.Fprintf(w, "%d:", i)
			node.print(w)
		}
	}
	fmt.Fprintf(w, ")")
}

func (n *duoNode) fstring(ind string) string {
	resp := fmt.Sprintf("duo[\n%s  ", ind)
	i1, i2 := n.childrenIdx()
	resp += fmt.Sprintf("%s: %v", indices[i1], n.child1.fstring(ind+"  "))
	resp += fmt.Sprintf("%s: %v", indices[i2], n.child2.fstring(ind+"  "))
	return resp + fmt.Sprintf("\n%s] ", ind)
}
func (n *duoNode) print(w io.Writer) {
	fmt.Fprintf(w, "d(")
	i1, i2 := n.childrenIdx()
	fmt.Fprintf(w, "%d:", i1)
	n.child1.print(w)
	fmt.Fprintf(w, "%d:", i2)
	n.child2.print(w)
	fmt.Fprintf(w, ")")
}

func (n *shortNode) fstring(ind string) string {
	return fmt.Sprintf("{%x: %v} ", n.Key, n.Val.fstring(ind+"  "))
}
func (n *shortNode) print(w io.Writer) {
	fmt.Fprintf(w, "s(%x:", n.Key)
	n.Val.print(w)
	fmt.Fprintf(w, ")")
}

func (n hashNode) fstring(ind string) string {
	return fmt.Sprintf("<%x> ", n.hash)
}
func (n hashNode) print(w io.Writer) {
	fmt.Fprintf(w, "h(%x)", n.hash)
}

func (n valueNode) fstring(ind string) string {
	return fmt.Sprintf("%x ", []byte(n))
}
func (n valueNode) print(w io.Writer) {
	fmt.Fprintf(w, "v(%x)", []byte(n))
}

func (n codeNode) fstring(ind string) string {
	return fmt.Sprintf("code: %x ", []byte(n))
}
func (n codeNode) print(w io.Writer) {
	fmt.Fprintf(w, "code(%x)", []byte(n))
}

func (an accountNode) fstring(ind string) string {
	s := fmt.Sprintf("acct(nonce=%d bal=%s)", an.Nonce, an.Balance.String())
	if an.storage != nil {
		s += " " + an.storage.fstring(ind+" ")
	}
	return s
}

func (an accountNode) print(w io.Writer) {
	fmt.Fprintf(w, "acct(nonce=%d bal=%s)", an.Nonce, an.Balance.String())
}
