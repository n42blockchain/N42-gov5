package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/crypto"
)

// PrepareQueueSpeculative sends a bounded advisory. Acceptance only means the
// owner retained one hint; it is neither a Build result nor durable execution.
func (c *Client) PrepareQueueSpeculative(ctx context.Context, request QueueBuildRequest) error {
	if request.Identity != c.identity || request.Epoch == ([16]byte{}) || len(request.PreparedRLP) == 0 || len(request.PreparedRLP) > MaxQueuePreparedBytes || request.MaxTransactions < 1 || request.MaxTransactions > MaxTransactions || request.MaxEncodedBytes < 1 || request.MaxEncodedBytes > MaxQueueBuildEncodedBytes || crypto.Keccak256Hash(request.PreparedRLP) != request.PreparedHash {
		return errors.New("invalid speculative queue binding or limits")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/prepare-queue", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("owner rejected speculative queue advisory: %s", bytes.TrimSpace(detail))
	}
	return nil
}

// DrainQueueSpeculative installs an owner-side epoch tombstone and waits for
// its current worker to stop. Recovery-only ACK requires this response.
func (c *Client) DrainQueueSpeculative(ctx context.Context, epoch [16]byte) error {
	if epoch == ([16]byte{}) {
		return errors.New("speculative drain epoch required")
	}
	raw, err := json.Marshal(DrainSpeculativeQueueRequest{Identity: c.identity, Epoch: epoch})
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/drain-prepare-queue", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("owner speculative drain failed: %s", bytes.TrimSpace(detail))
	}
	return nil
}
