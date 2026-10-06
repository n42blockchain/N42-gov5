//go:build linux

package mdbx

import (
	"errors"
	"fmt"

	mdbxlib "github.com/erigontech/mdbx-go/mdbx"
	"golang.org/x/sys/unix"
)

// StartWriteback asks Linux to start writing dirty mapped pages. It does not
// commit the transaction, flush metadata, or establish durability. MDBX's
// normal Commit remains responsible for synchronization and publication.
// Unsupported modes/platforms report requested=false and use ordinary Commit.
func (tx *MdbxTx) StartWriteback() (requested bool, err error) {
	return tx.startWriteback(unix.SyncFileRange)
}

func (tx *MdbxTx) startWriteback(writeRange func(int, int64, int64, int) error) (bool, error) {
	if tx.tx == nil || tx.readOnly {
		return false, errors.New("early writeback requires an active write transaction")
	}
	if !tx.db.opts.HasFlag(mdbxlib.WriteMap) {
		return false, nil
	}
	fd, err := tx.db.env.FD()
	if err != nil {
		return false, fmt.Errorf("get MDBX descriptor for early writeback: %w", err)
	}
	// Borrow the environment's descriptor; never close it. A zero length means
	// offset through EOF. WRITE initiates writeback without waiting for its
	// completion or advancing the descriptor's writeback-error cursor.
	err = writeRange(int(fd), 0, 0, unix.SYNC_FILE_RANGE_WRITE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return false, nil
	}
	if err != nil {
		// Do not hide I/O/space errors and subsequently advertise a commit.
		return false, fmt.Errorf("start MDBX data writeback: %w", err)
	}
	return true, nil
}
