package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/n42blockchain/N42/internal/sharedexec"
)

func strictIngressOptions(path string, maxTx, maxBytes int) (*sharedexec.StrictIngressOptions, error) {
	if path == "" {
		if maxTx != 0 || maxBytes != 0 {
			return nil, errors.New("ingress credits require explicit manifest")
		}
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("strict ingress manifest path must be absolute")
	}
	if maxTx == 0 {
		maxTx = 2_000_000
	}
	if maxBytes == 0 {
		maxBytes = 512 << 20
	}
	if maxTx < 1 || maxTx > 2_000_000 || maxBytes < 1 || maxBytes > 512<<20 {
		return nil, errors.New("strict ingress credits exceed bounded limits")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errors.New("strict ingress manifest must exist without symlinks")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > sharedexec.CorpusMaxManifestBytes {
		return nil, errors.New("strict ingress manifest regular-file size invalid")
	}
	// Nonblocking/no-follow also closes the check/open race: a replacement
	// FIFO or symlink cannot hang startup or redirect the manifest read.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, info) || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > sharedexec.CorpusMaxManifestBytes {
		return nil, errors.New("strict ingress manifest regular-file size invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, sharedexec.CorpusMaxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > sharedexec.CorpusMaxManifestBytes {
		return nil, errors.New("strict ingress manifest grew beyond limit")
	}
	if _, err := sharedexec.DecodeCorpusManifest(raw); err != nil {
		return nil, err
	}
	return &sharedexec.StrictIngressOptions{Manifest: raw, MaxTransactions: maxTx, MaxEncodedBytes: maxBytes}, nil
}
