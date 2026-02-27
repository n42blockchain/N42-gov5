package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/n42blockchain/N42/lib/common"
)

func logBase(n, base uint64) uint64 {
	return uint64(math.Ceil(math.Log(float64(n)) / math.Log(float64(base))))
}

type markupCursor struct {
	l  uint64 //l - level
	p  uint64 //p - pos inside level
	di uint64 //di - data array index
	si uint64 //si - current, actual son index
}

type node struct {
	p   uint64 // pos inside level
	d   uint64
	s   uint64 // sons pos inside level
	fc  uint64
	key []byte
	val []byte
}

type Cursor struct {
	ctx context.Context
	ix  *btAlloc

	key   []byte
	value []byte
	d     uint64
}

func (a *btAlloc) newCursor(ctx context.Context, k, v []byte, d uint64) *Cursor {
	return &Cursor{
		ctx:   ctx,
		key:   common.Copy(k),
		value: common.Copy(v),
		d:     d,
		ix:    a,
	}
}

func (c *Cursor) Key() []byte {
	return c.key
}

func (c *Cursor) Ordinal() uint64 {
	return c.d
}

func (c *Cursor) Value() []byte {
	return c.value
}

func (c *Cursor) Next() bool {
	if c.d > c.ix.K-1 {
		return false
	}
	k, v, err := c.ix.dataLookup(c.d + 1)
	if err != nil {
		return false
	}
	c.key = common.Copy(k)
	c.value = common.Copy(v)
	c.d++
	return true
}

type btAlloc struct {
	d       uint64 // depth
	M       uint64 // child limit of any node
	N       uint64
	K       uint64
	vx      []uint64   // vertex count on level
	sons    [][]uint64 // i - level; 0 <= i < d; j_k - amount, j_k+1 - child count
	cursors []markupCursor
	nodes   [][]node
	naccess uint64
	trace   bool

	dataLookup func(di uint64) ([]byte, []byte, error)
}

func newBtAlloc(k, M uint64, trace bool) *btAlloc {
	if k == 0 {
		return nil
	}

	d := logBase(k, M)
	a := &btAlloc{
		vx:      make([]uint64, d+1),
		sons:    make([][]uint64, d+1),
		cursors: make([]markupCursor, d),
		nodes:   make([][]node, d),
		M:       M,
		K:       k,
		d:       d,
		trace:   trace,
	}
	if trace {
		fmt.Printf("k=%d d=%d, M=%d\n", k, d, M)
	}
	a.vx[0], a.vx[d] = 1, k

	if k < M/2 {
		a.N = k
		a.nodes = make([][]node, 1)
		return a
	}

	nvc := func(vx uint64) uint64 {
		return uint64(math.Ceil(float64(vx) / float64(M>>1)))
	}

	for i := a.d - 1; i > 0; i-- {
		nnc := uint64(math.Ceil(float64(a.vx[i+1]) / float64(M)))
		a.vx[i] = min(uint64(math.Pow(float64(M), float64(i))), nnc)
	}

	ncount := uint64(0)
	pnv := uint64(0)
	for l := a.d - 1; l > 0; l-- {
		sh := nvc(a.vx[l+1])

		if sh&1 == 1 {
			a.sons[l] = append(a.sons[l], sh>>1, M, 1, M>>1)
		} else {
			a.sons[l] = append(a.sons[l], sh>>1, M)
		}

		for ik := 0; ik < len(a.sons[l]); ik += 2 {
			ncount += a.sons[l][ik] * a.sons[l][ik+1]
			if l == 1 {
				pnv += a.sons[l][ik]
			}
		}
	}
	a.sons[0] = []uint64{1, pnv}
	ncount += a.sons[0][0] * a.sons[0][1] // last one
	a.N = ncount

	if trace {
		for i, v := range a.sons {
			fmt.Printf("L%d=%v\n", i, v)
		}
	}

	return a
}

// nolint
// another implementation of traverseDfs supposed to be a bit cleaner but buggy yet
func (a *btAlloc) traverseTrick() {
	for l := 0; l < len(a.sons)-1; l++ {
		if len(a.sons[l]) < 2 {
			panic("invalid btree allocation markup")
		}
		a.cursors[l] = markupCursor{uint64(l), 1, 0, 0}
		a.nodes[l] = make([]node, 0)
	}

	lf := a.cursors[len(a.cursors)-1]
	c := a.cursors[(len(a.cursors) - 2)]

	var d uint64
	var fin bool

	lf.di = d
	lf.si++
	d++
	a.cursors[len(a.cursors)-1] = lf

	moved := true
	for int(c.p) <= len(a.sons[c.l]) {
		if fin || d > a.K {
			break
		}
		c, lf = a.cursors[c.l], a.cursors[lf.l]

		c.di = d
		c.si++

		sons := a.sons[lf.l][lf.p]
		for i := uint64(1); i < sons; i++ {
			lf.si++
			d++
		}
		lf.di = d
		d++

		a.nodes[lf.l] = append(a.nodes[lf.l], node{p: lf.p, s: lf.si, d: lf.di})
		a.nodes[c.l] = append(a.nodes[c.l], node{p: c.p, s: c.si, d: c.di})
		a.cursors[lf.l] = lf
		a.cursors[c.l] = c

		for l := lf.l; l >= 0; l-- {
			sc := a.cursors[l]
			sons, gsons := a.sons[sc.l][sc.p-1], a.sons[sc.l][sc.p]
			if l < c.l && moved {
				sc.di = d
				a.nodes[sc.l] = append(a.nodes[sc.l], node{d: sc.di})
				sc.si++
				d++
			}
			moved = (sc.si-1)/gsons != sc.si/gsons
			if sc.si/gsons >= sons {
				sz := uint64(len(a.sons[sc.l]) - 1)
				if sc.p+2 > sz {
					fin = l == lf.l
					break
				} else {
					sc.p += 2
					sc.si, sc.di = 0, 0
				}
				//moved = true
			}
			if l == lf.l {
				sc.si++
				sc.di = d
				d++
			}
			a.cursors[l] = sc
			if l == 0 {
				break
			}
		}
		moved = false
	}
}

func (a *btAlloc) traverseDfs() {
	for l := 0; l < len(a.sons)-1; l++ {
		a.cursors[l] = markupCursor{uint64(l), 1, 0, 0}
		a.nodes[l] = make([]node, 0)
	}

	if len(a.cursors) <= 1 {
		if a.nodes[0] == nil {
			a.nodes[0] = make([]node, 0)
		}
		a.nodes[0] = append(a.nodes[0], node{d: a.K})
		a.N = a.K
		if a.trace {
			fmt.Printf("ncount=%d ∂%.5f\n", a.N, float64(a.N-a.K)/float64(a.N))
		}
		return
	}

	c := a.cursors[len(a.cursors)-1]
	pc := a.cursors[(len(a.cursors) - 2)]
	root := new(node)
	trace := false

	var di uint64
	for stop := false; !stop; {
		// fill leaves, mark parent if needed (until all grandparents not marked up until root)
		// check if eldest parent has brothers
		//     -- has bros -> fill their leaves from the bottom
		//     -- no bros  -> shift cursor (tricky)
		if di > a.K {
			a.N = di - 1 // actually filled node count
			if a.trace {
				fmt.Printf("ncount=%d ∂%.5f\n", a.N, float64(a.N-a.K)/float64(a.N))
			}
			break
		}

		bros, parents := a.sons[c.l][c.p], a.sons[c.l][c.p-1]
		for i := uint64(0); i < bros; i++ {
			c.di = di
			if trace {
				fmt.Printf("L%d |%d| d %2d s %2d\n", c.l, c.p, c.di, c.si)
			}
			c.si++
			di++

			if i == 0 {
				pc.di = di
				if trace {
					fmt.Printf("P%d |%d| d %2d s %2d\n", pc.l, pc.p, pc.di, pc.si)
				}
				pc.si++
				di++
			}
			if di > a.K {
				a.N = di - 1 // actually filled node count
				stop = true
				break
			}
		}

		a.nodes[c.l] = append(a.nodes[c.l], node{p: c.p, d: c.di, s: c.si})
		a.nodes[pc.l] = append(a.nodes[pc.l], node{p: pc.p, d: pc.di, s: pc.si, fc: uint64(len(a.nodes[c.l]) - 1)})

		pid := c.si / bros
		if pid >= parents {
			if c.p+2 >= uint64(len(a.sons[c.l])) {
				stop = true // end of row
				if trace {
					fmt.Printf("F%d |%d| d %2d\n", c.l, c.p, c.di)
				}
			} else {
				c.p += 2
				c.si = 0
				c.di = 0
			}
		}
		a.cursors[c.l] = c
		a.cursors[pc.l] = pc

		//nolint
		for l := pc.l; l >= 0; l-- {
			pc := a.cursors[l]
			uncles := a.sons[pc.l][pc.p]
			grands := a.sons[pc.l][pc.p-1]

			pi1 := pc.si / uncles
			pc.si++
			pc.di = 0

			pi2 := pc.si / uncles
			moved := pi2-pi1 != 0

			switch {
			case pc.l > 0:
				gp := a.cursors[pc.l-1]
				if gp.di == 0 {
					gp.di = di
					di++
					if trace {
						fmt.Printf("P%d |%d| d %2d s %2d\n", gp.l, gp.p, gp.di, gp.si)
					}
					a.nodes[gp.l] = append(a.nodes[gp.l], node{p: gp.p, d: gp.di, s: gp.si, fc: uint64(len(a.nodes[l]) - 1)})
					a.cursors[gp.l] = gp
				}
			default:
				if root.d == 0 {
					root.d = di
					//di++
					if trace {
						fmt.Printf("ROOT | d %2d\n", root.d)
					}
				}
			}

			//fmt.Printf("P%d |%d| d %2d s %2d pid %d\n", pc.l, pc.p, pc.di, pc.si-1)
			if pi2 >= grands { // skip one step of si due to different parental filling order
				if pc.p+2 >= uint64(len(a.sons[pc.l])) {
					if trace {
						fmt.Printf("EoRow %d |%d|\n", pc.l, pc.p)
					}
					break // end of row
				}
				//fmt.Printf("N %d d%d s%d\n", pc.l, pc.di, pc.si)
				//fmt.Printf("P%d |%d| d %2d s %2d pid %d\n", pc.l, pc.p, pc.di, pc.si, pid)
				pc.p += 2
				pc.si = 0
				pc.di = 0
			}
			a.cursors[pc.l] = pc

			if !moved {
				break
			}
		}
	}

	if a.trace {
		fmt.Printf("ncount=%d ∂%.5f\n", a.N, float64(a.N-a.K)/float64(a.N))
	}
}

func (a *btAlloc) bsKey(x []byte, l, r uint64) (*Cursor, error) {
	for l <= r {
		di := (l + r) >> 1

		mk, value, err := a.dataLookup(di)
		a.naccess++

		cmp := bytes.Compare(mk, x)
		switch {
		case err != nil:
			if errors.Is(err, ErrBtIndexLookupBounds) {
				return nil, nil
			}
			return nil, err
		case cmp == 0:
			return a.newCursor(context.TODO(), mk, value, di), nil
		case cmp == -1:
			l = di + 1
		default:
			r = di
		}
		if l == r {
			break
		}
	}
	k, v, err := a.dataLookup(l)
	if err != nil {
		if errors.Is(err, ErrBtIndexLookupBounds) {
			return nil, nil
		}
		return nil, fmt.Errorf("key >= %x was not found. %w", x, err)
	}
	return a.newCursor(context.TODO(), k, v, l), nil
}

func (a *btAlloc) bsNode(i, l, r uint64, x []byte) (n node, lm int64, rm int64) {
	lm, rm = -1, -1
	var m uint64

	for l < r {
		m = (l + r) >> 1

		a.naccess++
		cmp := bytes.Compare(a.nodes[i][m].key, x)
		switch {
		case cmp == 0:
			return a.nodes[i][m], int64(m), int64(m)
		case cmp > 0:
			r = m
			rm = int64(m)
		case cmp < 0:
			lm = int64(m)
			l = m + 1
		default:
			panic(fmt.Errorf("compare error %d, %x ? %x", cmp, n.key, x))
		}
	}
	return a.nodes[i][m], lm, rm
}

// find position of key with node.di <= d at level lvl
func (a *btAlloc) seekLeast(lvl, d uint64) uint64 {
	for i := range a.nodes[lvl] {
		if a.nodes[lvl][i].d >= d {
			return uint64(i)
		}
	}
	return uint64(len(a.nodes[lvl]))
}

func (a *btAlloc) Seek(ik []byte) (*Cursor, error) {
	if a.trace {
		fmt.Printf("seek key %x\n", ik)
	}

	var (
		lm, rm     int64
		L, R       = uint64(0), uint64(len(a.nodes[0]) - 1)
		minD, maxD = uint64(0), a.K
		ln         node
	)

	for l, level := range a.nodes {
		if len(level) == 1 && l == 0 {
			ln = a.nodes[0][0]
			maxD = ln.d
			break
		}
		ln, lm, rm = a.bsNode(uint64(l), L, R, ik)
		if ln.key == nil { // should return node which is nearest to key from the left so never nil
			if a.trace {
				fmt.Printf("found nil key %x pos_range[%d-%d] naccess_ram=%d\n", l, lm, rm, a.naccess)
			}
			return nil, fmt.Errorf("bt index nil node at level %d", l)
		}

		switch bytes.Compare(ln.key, ik) {
		case 1: // key > ik
			maxD = ln.d
		case -1: // key < ik
			minD = ln.d
		case 0:
			if a.trace {
				fmt.Printf("found key %x v=%x naccess_ram=%d\n", ik, ln.val /*level[m].d,*/, a.naccess)
			}
			return a.newCursor(context.TODO(), common.Copy(ln.key), common.Copy(ln.val), ln.d), nil
		}

		if rm-lm >= 1 {
			break
		}
		if lm >= 0 {
			minD = a.nodes[l][lm].d
			L = level[lm].fc
		} else if l+1 != len(a.nodes) {
			L = a.seekLeast(uint64(l+1), minD)
			if L == uint64(len(a.nodes[l+1])) {
				L--
			}
		}
		if rm >= 0 {
			maxD = a.nodes[l][rm].d
			R = level[rm].fc
		} else if l+1 != len(a.nodes) {
			R = a.seekLeast(uint64(l+1), maxD)
			if R == uint64(len(a.nodes[l+1])) {
				R--
			}
		}

		if a.trace {
			fmt.Printf("range={%x d=%d p=%d} (%d, %d) L=%d naccess_ram=%d\n", ln.key, ln.d, ln.p, minD, maxD, l, a.naccess)
		}
	}

	a.naccess = 0 // reset count before actually go to disk
	cursor, err := a.bsKey(ik, minD, maxD)
	if err != nil {
		if a.trace {
			fmt.Printf("key %x not found\n", ik)
		}
		return nil, err
	}

	if a.trace {
		fmt.Printf("finally found key %x v=%x naccess_disk=%d\n", cursor.key, cursor.value, a.naccess)
	}
	return cursor, nil
}

func (a *btAlloc) fillSearchMx() {
	for i, n := range a.nodes {
		if a.trace {
			fmt.Printf("D%d |%d| ", i, len(n))
		}
		for j, s := range n {
			if a.trace {
				fmt.Printf("%d ", s.d)
			}
			if s.d >= a.K {
				break
			}

			kb, v, err := a.dataLookup(s.d)
			if err != nil {
				fmt.Printf("d %d not found %v\n", s.d, err)
			}
			a.nodes[i][j].key = common.Copy(kb)
			a.nodes[i][j].val = common.Copy(v)
		}
		if a.trace {
			fmt.Printf("\n")
		}
	}
}
