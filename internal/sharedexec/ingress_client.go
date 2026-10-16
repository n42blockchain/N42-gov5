package sharedexec

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// StrictIngressClient never reconnects or replays an ambiguous frame. Its
// successful ACK proves volatile admission, not canonical commitment.
type StrictIngressClient struct {
	mu       sync.Mutex
	conn     net.Conn
	identity Identity
	manifest types.Hash
	epoch    [16]byte
	shard    uint16
	broken   bool
}

func DialStrictIngress(ctx context.Context, path string, identity Identity, manifest types.Hash, shard uint16) (*StrictIngressClient, error) {
	if !filepath.IsAbs(path) || manifest == (types.Hash{}) || shard >= CorpusMaxShards {
		return nil, errors.New("invalid strict ingress endpoint or binding")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	client := &StrictIngressClient{conn: conn, identity: identity, manifest: manifest, shard: shard}
	if err := client.deadline(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := writeIngressBytes(conn, []byte("GSI1")); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := writeIngressMetadata(conn, ingressHello{Identity: identity, ManifestHash: manifest, Shard: shard}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var reply ingressReply
	if err := readIngressMetadata(conn, &reply); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if reply.Kind != "welcome" || reply.Identity != identity || reply.ManifestHash != manifest || reply.Epoch == ([16]byte{}) {
		_ = conn.Close()
		return nil, errors.New("strict ingress welcome binding mismatch")
	}
	client.epoch = reply.Epoch
	return client, nil
}
func (c *StrictIngressClient) deadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	return c.conn.SetDeadline(deadline)
}
func (c *StrictIngressClient) Close() error { return c.conn.Close() }

// Epoch is the immutable welcome binding, available before sending any frame.
func (c *StrictIngressClient) Epoch() [16]byte { return c.epoch }

// Admit sends a header, awaits a credit grant, then sends exactly one payload.
// Busy is a known zero-admission result. Any ambiguous IO/protocol outcome
// poisons this connection and requires external reconciliation before replay.
func (c *StrictIngressClient) Admit(ctx context.Context, frame CorpusFrame) (StrictIngressAck, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return StrictIngressAck{}, ErrIngressUnknownOutcome
	}
	if frame.Shard != c.shard {
		return StrictIngressAck{}, errors.New("strict ingress client shard mismatch")
	}
	header, err := frame.Header()
	if err != nil {
		return StrictIngressAck{}, err
	}
	if err := c.deadline(ctx); err != nil {
		return StrictIngressAck{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	defer stop()
	unknown := func(err error) (StrictIngressAck, error) {
		c.broken = true
		_ = c.conn.Close()
		return StrictIngressAck{}, fmt.Errorf("%w: %v", ErrIngressUnknownOutcome, err)
	}
	if err := writeIngressBytes(c.conn, header[:]); err != nil {
		return unknown(err)
	}
	read := func() (ingressReply, error) {
		var reply ingressReply
		if err := readIngressMetadata(c.conn, &reply); err != nil {
			return reply, err
		}
		if reply.Identity != c.identity || reply.ManifestHash != c.manifest || reply.Epoch != c.epoch || reply.FrameID != frame.CorpusFrameID || reply.PayloadHash != frame.PayloadHash {
			return reply, errors.New("strict ingress ACK binding mismatch")
		}
		if reply.Ack.Status.Identity != c.identity || reply.Ack.Status.ManifestHash != c.manifest || reply.Ack.Status.Epoch != c.epoch {
			return reply, errors.New("strict ingress ACK status binding mismatch")
		}
		if reply.Kind != "retry" && reply.RetryReason != "" {
			return reply, errors.New("unexpected strict ingress retry reason")
		}
		return reply, nil
	}
	retry := func(reply ingressReply) (StrictIngressAck, error) {
		if reply.RetryReason != ingressRetrySnapshot || reply.Ack.NewlyAdmitted != 0 || reply.Ack.Duplicates != 0 {
			return unknown(errors.New("invalid zero-admission snapshot retry"))
		}
		return reply.Ack, ErrIngressSnapshotChanged
	}
	reply, err := read()
	if err != nil {
		return unknown(err)
	}
	switch reply.Kind {
	case "retry":
		return retry(reply)
	case "busy":
		if reply.Ack.NewlyAdmitted != 0 || reply.Ack.Duplicates != 0 {
			return unknown(errors.New("busy ACK carried admission"))
		}
		return reply.Ack, ErrIngressCreditsExhausted
	case "rejected":
		c.broken = true
		_ = c.conn.Close()
		if reply.Ack.NewlyAdmitted != 0 || reply.Ack.Duplicates != 0 {
			return unknown(errors.New("rejected ACK carried admission"))
		}
		return reply.Ack, fmt.Errorf("strict ingress rejected: %s", reply.Error)
	case "duplicate":
		if reply.Ack.NewlyAdmitted != 0 || reply.Ack.Duplicates != int(frame.Transactions) {
			return unknown(errors.New("invalid duplicate ACK count"))
		}
		return reply.Ack, nil
	case "grant":
		if reply.Ack.NewlyAdmitted != 0 || reply.Ack.Duplicates != 0 {
			return unknown(errors.New("grant ACK carried admission"))
		}
	default:
		return unknown(errors.New("unexpected strict ingress header ACK"))
	}
	if err := writeIngressBytes(c.conn, frame.Payload); err != nil {
		return unknown(err)
	}
	reply, err = read()
	if err != nil {
		return unknown(err)
	}
	if reply.Kind == "retry" {
		return retry(reply)
	}
	if reply.Kind == "rejected" && reply.Ack.NewlyAdmitted == 0 && reply.Ack.Duplicates == 0 {
		c.broken = true
		_ = c.conn.Close()
		return reply.Ack, fmt.Errorf("strict ingress rejected: %s", reply.Error)
	}
	if reply.Kind != "admitted" || reply.Ack.NewlyAdmitted < 0 || reply.Ack.Duplicates < 0 || reply.Ack.NewlyAdmitted+reply.Ack.Duplicates != int(frame.Transactions) {
		return unknown(errors.New("invalid final admission ACK"))
	}
	return reply.Ack, nil
}
