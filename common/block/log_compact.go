// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Compact STORAGE codec for the per-transaction log list — companion to the
// compact header, transaction and receipt codecs, and the last write path in
// the receipt family that still emitted protobuf.
//
// The proto encoding spent most of its bytes on fields that are not log
// content. Every log carried BlockNumber, TxHash, BlockHash, TxIndex and Index,
// and each of those is either already in the key (the table is keyed by
// block number + transaction id) or derivable from position. Wrapping them in
// H160/H256 messages cost a further length prefix and field tag apiece. What a
// log actually is, and all the consensus content there is, is the emitting
// address, its topics and its data.
//
//	[0] 0xFF  — format marker (a valid protobuf message can never begin with
//	            0xFF: field 31 / wire type 7 is illegal, which is what lets
//	            Unmarshal dispatch between the two formats)
//	[1] 0x01  — codec version
//	[2..] uvarint count, then per log:
//	      address 20 B
//	      uvarint topic count, topics 32 B each
//	      uvarint data length, data
//
// This is byte-for-byte the log encoding already embedded in the compact
// receipts codec, so the two agree on what a stored log is.
//
// Decoded logs carry Address, Topics and Data; the context fields stay zero.
// That is the same contract the compact receipts codec documents for its raw
// read path, and the caller that needs them (ReadReceipts) fills them in from
// the block.

package block

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

const (
	compactLogsMarker  = 0xFF
	compactLogsVersion = 0x01
)

// MarshalCompact encodes the log list in the compact storage format.
func (l *Logs) MarshalCompact() []byte {
	logs := *l
	// Header, count, and a per-log estimate of address + one topic + a short
	// data run; append grows past it when a log carries more.
	buf := make([]byte, 0, 2+binary.MaxVarintLen64+len(logs)*(20+1+32+1+32))
	buf = append(buf, compactLogsMarker, compactLogsVersion)
	buf = rcAppendUvarint(buf, uint64(len(logs)))
	for _, lg := range logs {
		buf = append(buf, lg.Address[:]...)
		buf = rcAppendUvarint(buf, uint64(len(lg.Topics)))
		for _, t := range lg.Topics {
			buf = append(buf, t[:]...)
		}
		buf = rcAppendUvarint(buf, uint64(len(lg.Data)))
		buf = append(buf, lg.Data...)
	}
	return buf
}

// IsCompactLogs reports whether data is in the compact log format.
func IsCompactLogs(data []byte) bool {
	return len(data) >= 2 && data[0] == compactLogsMarker && data[1] == compactLogsVersion
}

// unmarshalCompact decodes the compact log format. Context fields stay zero —
// see the file comment.
func (l *Logs) unmarshalCompact(data []byte) error {
	if !IsCompactLogs(data) {
		return fmt.Errorf("not a compact log record")
	}
	pos := 2

	take := func(n int) ([]byte, error) {
		if n < 0 || pos+n > len(data) {
			return nil, fmt.Errorf("compact logs truncated: need %d bytes at offset %d of %d", n, pos, len(data))
		}
		b := data[pos : pos+n]
		pos += n
		return b, nil
	}
	uvarint := func() (uint64, error) {
		v, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return 0, fmt.Errorf("compact logs: bad uvarint at offset %d", pos)
		}
		pos += n
		return v, nil
	}

	count, err := uvarint()
	if err != nil {
		return err
	}
	// A log is at least an address plus two uvarints, so a count implying more
	// bytes than remain is malformed. Checking it up front keeps a corrupt
	// length from being turned into a huge allocation.
	if count > uint64(len(data)-pos) {
		return fmt.Errorf("compact logs: count %d exceeds remaining %d bytes", count, len(data)-pos)
	}

	logs := make([]*Log, 0, count)
	for i := uint64(0); i < count; i++ {
		lg := new(Log)

		addr, err := take(types.AddressLength)
		if err != nil {
			return err
		}
		copy(lg.Address[:], addr)

		topicCount, err := uvarint()
		if err != nil {
			return err
		}
		if topicCount > uint64(len(data)-pos)/types.HashLength {
			return fmt.Errorf("compact logs: topic count %d exceeds remaining bytes", topicCount)
		}
		if topicCount > 0 {
			lg.Topics = make([]types.Hash, topicCount)
			for j := uint64(0); j < topicCount; j++ {
				t, err := take(types.HashLength)
				if err != nil {
					return err
				}
				copy(lg.Topics[j][:], t)
			}
		}

		dataLen, err := uvarint()
		if err != nil {
			return err
		}
		raw, err := take(int(dataLen))
		if err != nil {
			return err
		}
		if dataLen > 0 {
			lg.Data = append([]byte(nil), raw...)
		}

		logs = append(logs, lg)
	}
	*l = logs
	return nil
}
