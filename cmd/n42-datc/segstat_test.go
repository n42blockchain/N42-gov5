package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	log "github.com/n42blockchain/N42/lib/log/v3"
)

// TestSegStat prints record-kind statistics of a finished build's account
// node segments (DATC_SEGSTAT_DIR=<out dir>); skipped otherwise.
func TestSegStat(t *testing.T) {
	dir := os.Getenv("DATC_SEGSTAT_DIR")
	if dir == "" {
		t.Skip("DATC_SEGSTAT_DIR not set")
	}
	set, ok, err := openLeafSegSet(dir, segTabNodeA, newFrameLRU())
	if err != nil || !ok {
		t.Fatalf("open na: ok=%v err=%v", ok, err)
	}
	defer set.Close()
	type st struct{ full, diff, mixed, tomb, bytes uint64 }
	byDepth := map[int]*st{}
	c := set.Cursor()
	rows := 0
	for k, v, e := c.Seek([]byte{0}); k != nil && e == nil; k, v, e = c.Next() {
		d := int(k[0])
		s := byDepth[d]
		if s == nil {
			s = &st{}
			byDepth[d] = s
		}
		s.bytes += uint64(len(k) + len(v))
		switch {
		case len(v) == 0:
			s.tomb++
		case v[0] == nodeRecFull:
			s.full++
		case v[0] == nodeRecDiff:
			s.diff++
		case v[0] == nodeRecMixed:
			s.mixed++
		}
		rows++
	}
	for d := 0; d <= maxChgDepth; d++ {
		if s := byDepth[d]; s != nil {
			fmt.Printf("depth %d: full=%d diff=%d mixed=%d tomb=%d bytes=%d\n", d, s.full, s.diff, s.mixed, s.tomb, s.bytes)
		}
	}
	fmt.Printf("rows=%d\n", rows)
	// A few depth-3 FULL records: how many children hashed vs present.
	c2 := set.Cursor()
	shown := 0
	for k, v, e := c2.Seek([]byte{3}); k != nil && e == nil && shown < 5; k, v, e = c2.Next() {
		if len(v) > 7 && v[0] == nodeRecFull {
			hs := binary.BigEndian.Uint16(v[1:])
			hh := binary.BigEndian.Uint16(v[5:])
			fmt.Printf("  d3 FULL path=%x epoch=%d hasState=%016b hasHash=%016b\n", k[1:4], binary.BigEndian.Uint32(k[4:]), hs, hh)
			shown++
		}
	}
}

// TestSegFloorDiag: for DATC_SEGSTAT_DIR and DATC_SEGSTAT_N, run floorRecord
// over every depth-2 path and dump the raw cursor neighbourhood of failures.
func TestSegFloorDiag(t *testing.T) {
	dir := os.Getenv("DATC_SEGSTAT_DIR")
	if dir == "" {
		t.Skip("DATC_SEGSTAT_DIR not set")
	}
	var n uint64
	fmt.Sscanf(os.Getenv("DATC_SEGSTAT_N"), "%d", &n)
	modulesInit()
	db, err := openDatcDB(log.New(), dir, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q, head, err := loadQuerier(tx, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("head=%d N=%d sched=%v segNA=%v\n", head, n, q.sched.e, q.segNA != nil)
	okN, fail := 0, 0
	for a := 0; a < 16; a++ {
		for b := 0; b < 16; b++ {
			path := []byte{byte(a), byte(b)}
			st, ep, ok, err := q.floorRecord(nil, path, n)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				okN++
				continue
			}
			fail++
			if fail <= 3 {
				fmt.Printf("FAIL path=%x epoch=%d hasState=%04x hasHash=%04x\n", path, ep, st.hasState, st.hasHash)
				c, _ := q.nodeCursor(false)
				seek := append([]byte{2, byte(a), byte(b)}, 0, 0, 0, 0)
				binary.BigEndian.PutUint32(seek[3:], uint32(q.sched.epochOf(2, n)+1))
				k, v, _ := c.Seek(seek)
				fmt.Printf("  seek=%x -> k=%x vlen=%d\n", seek, k, len(v))
				for i := 0; i < 4; i++ {
					k, v, _ = c.Prev()
					if k == nil {
						fmt.Printf("  prev -> nil\n")
						break
					}
					flag := -1
					if len(v) > 0 {
						flag = int(v[0])
					}
					fmt.Printf("  prev -> k=%x flag=%d vlen=%d\n", k, flag, len(v))
				}
				c.Close()
			}
		}
	}
	fmt.Printf("d2 paths: ok=%d fail=%d\n", okN, fail)
}
