package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// Each worker owns its connections. A frame is encoded once and reused for
// every destination; the pool still verifies the sender carried in the frame.
type ingestClient struct {
	addr string
	conn net.Conn
	rust bool
}

func (c *ingestClient) close() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func encodeIngestFrame(raws []string, senders []types.Address) ([]byte, error) {
	return encodeIngestProtocolFrame(raws, senders, false)
}

// n42-rs carries EIP-2718 bytes with u32 lengths and no sender trailer.
// gov5 carries native bytes with u16 lengths and a checked sender claim.
func encodeIngestProtocolFrame(raws []string, senders []types.Address, rust bool) ([]byte, error) {
	if (!rust && len(raws) != len(senders)) || len(raws) == 0 || len(raws) > 100000 || (rust && len(raws) > 10000) {
		return nil, fmt.Errorf("invalid ingest batch shape")
	}
	frame := binary.LittleEndian.AppendUint32(nil, uint32(len(raws)))
	for i, raw := range raws {
		if !strings.HasPrefix(raw, "0x") {
			return nil, fmt.Errorf("ingest transaction %d is not hex encoded", i)
		}
		encoded, err := hex.DecodeString(raw[2:])
		if err != nil {
			return nil, err
		}
		if len(encoded) == 0 || len(encoded) > 48*1024 {
			return nil, fmt.Errorf("ingest transaction %d has invalid size %d", i, len(encoded))
		}
		if rust {
			frame = binary.LittleEndian.AppendUint32(frame, uint32(len(encoded)))
		} else {
			frame = binary.LittleEndian.AppendUint16(frame, uint16(len(encoded)))
		}
		frame = append(frame, encoded...)
		if !rust {
			frame = append(frame, senders[i][:]...)
		}
	}
	return frame, nil
}

func (c *ingestClient) submit(frame []byte, count int) (err error) {
	if c.conn == nil {
		c.conn, err = net.DialTimeout("tcp", c.addr, 10*time.Second)
		if err != nil {
			return err
		}
	}
	defer func() {
		if err != nil {
			c.close()
		}
	}()
	if err = c.conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	for remaining := frame; len(remaining) > 0; {
		var n int
		n, err = c.conn.Write(remaining)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[n:]
	}
	var response [8]byte
	responseSize := 4
	if c.rust {
		responseSize = 8 // Drain pool_pending too, preserving framing for the next reply.
	}
	if _, err = io.ReadFull(c.conn, response[:responseSize]); err != nil {
		return err
	}
	accepted := binary.LittleEndian.Uint32(response[:])
	if uint64(accepted) != uint64(count) {
		return fmt.Errorf("ingest accepted %d/%d transactions; count-only response cannot identify rejected nonces", accepted, count)
	}
	return nil
}
