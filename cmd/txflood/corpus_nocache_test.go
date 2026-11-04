package main

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// residentPages counts path's pages in the page cache (mmap + mincore).
func residentPages(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	if fi.Size() == 0 {
		return 0
	}
	m, err := unix.Mmap(int(f.Fd()), 0, int(fi.Size()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(m)
	pg := os.Getpagesize()
	vec := make([]byte, (len(m)+pg-1)/pg)
	if _, _, e := unix.Syscall(unix.SYS_MINCORE, uintptr(unsafe.Pointer(&m[0])), uintptr(len(m)), uintptr(unsafe.Pointer(&vec[0]))); e != 0 {
		t.Fatal(e)
	}
	n := 0
	for _, v := range vec {
		n += int(v & 1)
	}
	return n
}

// TestCorpusNoCache: with -corpus-nocache the corpus leaves the page cache
// after it is written and after it is streamed, and the stream is unchanged.
func TestCorpusNoCache(t *testing.T) {
	dir := t.TempDir()
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err == nil && fs.Type == unix.TMPFS_MAGIC {
		// tmpfs pages are the file: DONTNEED cannot drop them.
		if d := os.Getenv("N42_TEST_DISK_DIR"); d != "" {
			dir, _ = os.MkdirTemp(d, "nocache")
			defer os.RemoveAll(dir)
		} else {
			t.Skip("temp dir is tmpfs; set N42_TEST_DISK_DIR to a disk-backed directory")
		}
	}
	defer func(s int64) { corpusDropStep = s }(corpusDropStep)
	corpusDropStep = 1 << 16

	const total = 200000
	rawAt := func(i int64) []byte { return []byte{0xAB, byte(i), byte(i >> 8), byte(i >> 16)} }
	for _, noCache := range []bool{false, true} {
		_, st, stop, sink := sinkServer(t)
		p := filepath.Join(dir, "c.bin")
		sink.total, sink.rawAt, sink.workers, sink.corpusNoCache = total, rawAt, 4, noCache
		if err := sink.writeCorpus(p); err != nil {
			t.Fatal(err)
		}
		if n := residentPages(t, p); noCache && n > 2 {
			t.Fatalf("%d pages cached after a no-cache write", n)
		}
		c, err := openCorpus(p)
		if err != nil {
			t.Fatal(err)
		}
		c.noCache = noCache
		sink.corpus = c
		res := sink.run(false)
		stop()
		if res.Txs != total || res.Failed != 0 || int64(st.Len()) != res.Frames {
			t.Fatalf("noCache=%v: %s store=%d", noCache, res, st.Len())
		}
		n := residentPages(t, p)
		t.Logf("noCache=%v: %d frames, %d pages cached after the stream", noCache, res.Frames, n)
		if !noCache && n == 0 {
			t.Skip("filesystem keeps nothing cached after a read; the check means nothing here")
		}
		if noCache && n > 2 {
			t.Fatalf("%d pages cached after a no-cache stream", n)
		}
		os.Remove(p)
	}
}
