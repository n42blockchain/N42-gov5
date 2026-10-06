package verify

import (
	"errors"
	"fmt"
	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/receipt"
	d "github.com/n42blockchain/N42/internal/ddn/types"
	"sync"
)

// Verifier is a bounded consumer replay cache. Production consumers must
// persist consumed nonces with their settlement transaction across restarts.
type Verifier struct {
	mu   sync.Mutex
	max  int
	seen map[string]uint64
}

func New(max int) (*Verifier, error) {
	if max < 1 || max > 100000 {
		return nil, errors.New("invalid replay cache capacity")
	}
	return &Verifier{max: max, seen: map[string]uint64{}}, nil
}
func (v *Verifier) Consume(r d.DecisionReceipt, req d.DecisionRequest, expected chain.Address, now uint64) error {
	if err := receipt.Verify(r, req, expected, now); err != nil {
		return err
	}
	key := fmt.Sprintf("%d/%s/%d/%s", req.ChainID, req.Requester, req.Nonce, expected.Hex())
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, expiry := range v.seen {
		if expiry <= now {
			delete(v.seen, k)
		}
	}
	if _, ok := v.seen[key]; ok {
		return errors.New("DDN receipt replay")
	}
	if len(v.seen) >= v.max {
		return errors.New("DDN replay cache full")
	}
	v.seen[key] = req.Deadline
	return nil
}
