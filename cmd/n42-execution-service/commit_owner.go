package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// This local owner secret is independent of validator signing keys. Provision
// it outside the repository and disclose it only to the trusted finality owner.
func loadCommitOwner(path string) ([32]byte, error) {
	var owner [32]byte
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || real != path {
		return owner, errors.New("commit owner file must be absolute without symlinks")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return owner, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return owner, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Getuid()) {
		return owner, errors.New("commit owner file requires a regular current-user file with mode 0600")
	}
	data, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(data) != 32 {
		return owner, errors.New("commit owner file requires exactly 32 random bytes")
	}
	copy(owner[:], data)
	if owner == ([32]byte{}) {
		return owner, errors.New("commit owner capability must be nonzero")
	}
	return owner, nil
}
