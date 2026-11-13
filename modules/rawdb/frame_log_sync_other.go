// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !linux

package rawdb

import "os"

// frameLogSync makes the file durable.
func frameLogSync(f *os.File) error { return f.Sync() }

func frameLogPreallocate(*os.File, int64) {}
