//go:build !linux

package mdbx

// StartWriteback is unavailable here. The normal Commit path is unchanged.
func (tx *MdbxTx) StartWriteback() (bool, error) { return false, nil }
