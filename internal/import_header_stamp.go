// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import "github.com/n42blockchain/N42/internal/consensus"

// headerVerifyLogFields returns the split of the most recent header
// verification for the "blockimport phases" line (r97a stamps). hdr on that
// line is the import's wait for the VerifyHeaders result; these are the
// verification's own parts, run on VerifyHeaders' goroutine: hdrVerN the
// header they belong to (compare with n), hdrParentUs the parent lookup,
// hdrGasUs the gas-used bound's ancestor walk, hdrQcUs the QC check,
// hdrCeUs the committee-evidence link, hdrAllUs the whole call, hdrQcCache
// how the QC check was answered (N42_IMPORT_HEADER_VERIFY_CACHE).
func headerVerifyLogFields() []any {
	s := consensus.LastHeaderVerify()
	return []any{"hdrVerN", s.Number, "hdrParentUs", s.Parent.Microseconds(), "hdrGasUs", s.Gas.Microseconds(),
		"hdrQcUs", s.QC.Microseconds(), "hdrCeUs", s.CE.Microseconds(), "hdrAllUs", s.All.Microseconds(),
		"hdrQcCache", s.QCCache}
}
