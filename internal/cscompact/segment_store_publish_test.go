package cscompact

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// Each failure boundary is checked in a fresh process. A published cidx is
// always a complete generation; a failure before rename leaves the old one.
func TestSegmentStorePublicationAfterInterruptedStage(t *testing.T) {
	if dir := os.Getenv("CSCOMPACT_TEST_REOPEN_DIR"); dir != "" {
		store, err := OpenSegmentStore(dir, "txindex")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		want, err := strconv.ParseUint(os.Getenv("CSCOMPACT_TEST_REOPEN_COUNT"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := store.SegmentCount(); got != want {
			t.Fatalf("fresh-process segment count = %d, want %d", got, want)
		}
		for i := uint64(0); i < want; i++ {
			data, err := store.ReadSegmentData(i)
			if err != nil || string(data) != os.Getenv("CSCOMPACT_TEST_REOPEN_DATA") {
				t.Fatalf("fresh-process segment %d = %q, %v", i, data, err)
			}
		}
		return
	}

	for _, tc := range []struct {
		stage string
		want  uint64
	}{
		{stage: "data-durable", want: 0},
		{stage: "index-temp-durable", want: 0},
		{stage: "index-renamed", want: 1},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			dir := t.TempDir()
			w, err := NewSegmentStoreWriter(dir, "txindex")
			if err != nil {
				t.Fatal(err)
			}
			w.fault = func(stage string) error {
				if stage == tc.stage {
					return errors.New("injected power loss")
				}
				return nil
			}
			if _, err := w.WriteSegment([]byte("durable frame"), ""); err == nil {
				t.Fatal("interrupted write reported success")
			}
			if _, err := w.WriteSegment([]byte("retry"), ""); err == nil {
				t.Fatal("ambiguous writer allowed a blind retry")
			}
			w.Close()
			reopen := func(want uint64, data string) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestSegmentStorePublicationAfterInterruptedStage$")
				cmd.Env = append(os.Environ(), "CSCOMPACT_TEST_REOPEN_DIR="+dir,
					"CSCOMPACT_TEST_REOPEN_COUNT="+strconv.FormatUint(want, 10),
					"CSCOMPACT_TEST_REOPEN_DATA="+data)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fresh-process reopen: %v\n%s", err, out)
				}
			}
			reopen(tc.want, "durable frame")
			if tc.want == 0 {
				// Restarting after an orphan cdat frame must append at the
				// physical end, not point the new cidx entry at old bytes.
				next, err := NewSegmentStoreWriter(dir, "txindex")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := next.WriteSegment([]byte("new frame"), ""); err != nil {
					t.Fatal(err)
				}
				next.Close()
				reopen(1, "new frame")
			}
		})
	}
}

func TestSegmentStoreProcessExitAtPublication(t *testing.T) {
	if dir := os.Getenv("CSCOMPACT_TEST_DIE_DIR"); dir != "" {
		w, err := NewSegmentStoreWriter(dir, "txindex")
		if err != nil {
			t.Fatal(err)
		}
		stage := os.Getenv("CSCOMPACT_TEST_DIE_STAGE")
		w.fault = func(at string) error {
			if at == stage {
				os.Exit(0)
			}
			return nil
		}
		if _, err := w.WriteSegment([]byte("durable frame"), ""); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("faultpoint %s did not stop the process", stage)
	}
	for _, tc := range []struct {
		stage string
		want  string
	}{
		{stage: "data-durable", want: "0"},
		{stage: "index-temp-durable", want: "0"},
		{stage: "index-renamed", want: "1"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			dir := t.TempDir()
			child := exec.Command(os.Args[0], "-test.run=^TestSegmentStoreProcessExitAtPublication$")
			child.Env = append(os.Environ(), "CSCOMPACT_TEST_DIE_DIR="+dir, "CSCOMPACT_TEST_DIE_STAGE="+tc.stage)
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("writer process: %v\n%s", err, out)
			}
			reopen := exec.Command(os.Args[0], "-test.run=^TestSegmentStorePublicationAfterInterruptedStage$")
			reopen.Env = append(os.Environ(), "CSCOMPACT_TEST_REOPEN_DIR="+dir,
				"CSCOMPACT_TEST_REOPEN_COUNT="+tc.want,
				"CSCOMPACT_TEST_REOPEN_DATA=durable frame")
			if out, err := reopen.CombinedOutput(); err != nil {
				t.Fatalf("fresh-process reopen: %v\n%s", err, out)
			}
		})
	}
}
