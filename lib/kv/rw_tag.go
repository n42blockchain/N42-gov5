// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package kv

import "context"

// Write-transaction holder tags.
//
// An environment has one writer. A write transaction begun with a context
// carrying a tag (WithRwTag) is accounted under that tag by the MDBX
// layer's writer-lock statistics: who held the writer while another write
// waited to begin, how often and for how long. Untagged write transactions
// are accounted under their caller's name when the holder diagnostics are on
// (N42_RW_HOLDER_DIAG=1), else as "other".

type rwTagKey struct{}

// WithRwTag returns ctx tagged with tag for the write transactions begun
// with it.
func WithRwTag(ctx context.Context, tag string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, rwTagKey{}, tag)
}

// RwTagFrom returns ctx's write-transaction tag ("" when none).
func RwTagFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(rwTagKey{}).(string); ok {
		return v
	}
	return ""
}
