package mptbuild

import "errors"

var (
	errShortStorageValue = errors.New("mptbuild: storage value < 32 bytes (missing slot prefix)")
	ErrEmptySource       = errors.New("mptbuild: source cursor returned 0 rows")
)
