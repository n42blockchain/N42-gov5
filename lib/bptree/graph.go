/*
   Copyright 2022 Erigon contributors

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

package bptree

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
)

var palette = []string{"#FDF3D0", "#DCE8FA", "#D9E7D6", "#F1CFCD", "#F5F5F5", "#E1D5E7", "#FFE6CC", "white"}

const (
	colorUnexposed = 0
	colorExposed   = 1
	colorUpdated   = 2
)

// Node23Graph generates Graphviz DOT representations of a 2-3 tree.
type Node23Graph struct {
	node *Node23
}

func NewGraph(node *Node23) *Node23Graph {
	return &Node23Graph{node}
}

func (g *Node23Graph) saveDot(filename string, debug bool) {
	f, err := os.OpenFile(filename+".dot", os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
	}()

	if g.node == nil {
		writeOrDie(f, "strict digraph {\nnode [shape=record];}\n")
		return
	}

	writeOrDie(f, "strict digraph {\nnode [shape=record];\n")

	for _, n := range g.node.walkNodesPostOrder() {
		left, down, right := nodeSlotLabels(n)
		nodeID := nodeLabel(n, debug)
		color := nodeColor(n)
		s := fmt.Sprintf("%d [label=\"%s|{<C>%s|%s}|%s\" style=filled fillcolor=\"%s\"];\n",
			n.rawPointer(), left, nodeID, down, right, color)
		writeOrDie(f, s)
	}

	for _, n := range g.node.walkNodesPostOrder() {
		writeEdges(f, n)
	}

	writeOrDie(f, "}\n")
}

func (g *Node23Graph) saveDotAndPicture(filename string, debug bool) error {
	graphDir := "testdata/graph/"
	_ = os.MkdirAll(graphDir, os.ModePerm)
	filepath := graphDir + filename
	_ = os.Remove(filepath + ".dot")
	_ = os.Remove(filepath + ".png")
	g.saveDot(filepath, debug)

	dotExecutable, _ := exec.LookPath("dot")
	cmdDot := &exec.Cmd{
		Path:   dotExecutable,
		Args:   []string{dotExecutable, "-Tpng", filepath + ".dot", "-o", filepath + ".png"},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	return cmdDot.Run()
}

func nodeSlotLabels(n *Node23) (left, down, right string) {
	switch n.childrenCount() {
	case 1:
		left = "<L>L"
	case 2:
		left = "<L>L"
		right = "<R>R"
	case 3:
		left = "<L>L"
		down = "<D>D"
		right = "<R>R"
	}
	return left, down, right
}

func nodeLabel(n *Node23, debug bool) string {
	if n.isLeaf {
		if n.keyCount() == 0 {
			return "k=[]"
		}
		var next string
		if n.nextKey() == nil {
			next = "nil"
		} else {
			next = strconv.FormatUint(uint64(*n.nextKey()), 10)
		}
		if debug {
			return fmt.Sprintf("k=%v %s-%v", deref(n.keys[:len(n.keys)-1]), next, n.keys)
		}
		return fmt.Sprintf("k=%v %s", deref(n.keys[:len(n.keys)-1]), next)
	}
	if debug {
		return fmt.Sprintf("k=%v-%v", deref(n.keys), n.keys)
	}
	return fmt.Sprintf("k=%v", deref(n.keys))
}

func nodeColor(n *Node23) string {
	if n.exposed {
		if n.updated {
			return palette[colorUpdated]
		}
		return palette[colorExposed]
	}
	ensure(!n.updated, fmt.Sprintf("saveDot: node %v is not exposed but updated", n))
	return palette[colorUnexposed]
}

func writeEdges(f *os.File, n *Node23) {
	var children [3]*Node23
	switch n.childrenCount() {
	case 1:
		children[0] = n.children[0]
	case 2:
		children[0] = n.children[0]
		children[2] = n.children[1]
	case 3:
		children[0] = n.children[0]
		children[1] = n.children[1]
		children[2] = n.children[2]
	}

	labels := [3]string{"L", "D", "R"}
	for i, child := range children {
		if child != nil {
			writeOrDie(f, fmt.Sprintf("%d:%s -> %d:C;\n", n.rawPointer(), labels[i], child.rawPointer()))
		}
	}
}

func writeOrDie(f *os.File, s string) {
	if _, err := f.WriteString(s); err != nil {
		log.Fatal(err)
	}
}
