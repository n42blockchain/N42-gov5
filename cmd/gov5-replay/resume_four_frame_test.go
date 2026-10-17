package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/sharedexec"
)

func TestResumeFourFrameKnownZeroUsesFreshDeadlineAndUnknownStops(t *testing.T) {
	var first context.Context
	calls := 0
	frame := sharedexec.CorpusFrame{}
	selected := sharedexec.ResumeFrame{}
	ack, retries, err := admitResumeFourFrame(context.Background(), frame, selected, func(ctx context.Context, got sharedexec.CorpusFrame, plan sharedexec.ResumeFrame) (sharedexec.ResumeIngressAck, error) {
		calls++
		if calls == 1 {
			first = ctx
			return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressCreditsExhausted
		}
		if first.Err() != context.Canceled || ctx.Err() != nil {
			t.Fatal("retry reused expired/canceled I/O context")
		}
		a, _ := first.Deadline()
		b, _ := ctx.Deadline()
		if !b.After(a) || time.Until(b) < 4*time.Second {
			t.Fatal("retry did not receive fresh bounded deadline")
		}
		return sharedexec.ResumeIngressAck{NewlyAdmitted: 459}, nil
	})
	if err != nil || ack.NewlyAdmitted != 459 || calls != 2 || retries != 1 {
		t.Fatalf("typed zero retry: %+v %d %d %v", ack, retries, calls, err)
	}
	calls = 0
	_, _, err = admitResumeFourFrame(context.Background(), frame, selected, func(context.Context, sharedexec.CorpusFrame, sharedexec.ResumeFrame) (sharedexec.ResumeIngressAck, error) {
		calls++
		return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressUnknownOutcome
	})
	if !errors.Is(err, sharedexec.ErrIngressUnknownOutcome) || calls != 1 {
		t.Fatalf("unknown outcome was replayed: %d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	_, _, err = admitResumeFourFrame(ctx, frame, selected, func(context.Context, sharedexec.CorpusFrame, sharedexec.ResumeFrame) (sharedexec.ResumeIngressAck, error) {
		calls++
		cancel()
		return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressSnapshotChanged
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled zero outcome replayed: %d %v", calls, err)
	}
}

func TestResumeFourFrameReadsOriginalBytesAndPinsOpenFiles(t *testing.T) {
	f := makeResumeSourceFixture(t)
	scan, err := scanResumeSources(context.Background(), f.root, f.manifest, f.genesis, f.baseline)
	if err != nil {
		t.Fatal(err)
	}
	frames, check, closeFiles, err := readResumeFourFrames(context.Background(), f.root, f.manifest, scan.Frames)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles()
	if len(frames) != 4 || check() != nil {
		t.Fatal("original frame read failed")
	}
	for i, frame := range frames {
		if frame.CorpusFrameID != scan.Frames[i].ID || frame.PayloadHash != scan.Frames[i].PayloadHash || len(frame.Payload) != int(scan.Frames[i].PayloadBytes) {
			t.Fatal("original payload changed")
		}
	}
	path := filepath.Join(f.root, f.manifest.Shards[scan.Frames[0].ID.Shard].File)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if check() == nil {
		t.Fatal("source mutation accepted after original read")
	}
	closeFiles()
	if check() == nil {
		t.Fatal("closed descriptors remained usable")
	}
	wrong := append([]sharedexec.ResumeFrame(nil), scan.Frames...)
	wrong[0].PayloadHash[0] ^= 1
	if _, _, closeWrong, err := readResumeFourFrames(context.Background(), f.root, f.manifest, wrong); err == nil || closeWrong != nil {
		t.Fatal("wrong original payload returned a successful descriptor lease")
	}
}
