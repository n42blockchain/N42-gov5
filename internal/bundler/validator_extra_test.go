package bundler

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// fakeSessionValidator is a controllable AgentSessionValidator for testing
// ValidateAgentSession without depending on the real ai/wallet package.
type fakeSessionValidator struct {
	called    bool
	keyID     types.Hash
	target    types.Address
	methodSig []byte
	gas       uint64
	err       error
}

func (f *fakeSessionValidator) ValidateSessionKey(keyID types.Hash, target types.Address, methodSig []byte, value *uint256.Int, gas uint64) error {
	f.called = true
	f.keyID = keyID
	f.target = target
	f.methodSig = append([]byte(nil), methodSig...)
	f.gas = gas
	return f.err
}

// TestValidateAgentSession_NilValidatorSkips covers the nil-validator
// short-circuit: no error and no call attempted.
func TestValidateAgentSession_NilValidatorSkips(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0x01"), 0)
	if err := v.ValidateAgentSession(op, nil); err != nil {
		t.Fatalf("ValidateAgentSession() error = %v, want nil", err)
	}
}

// TestValidateAgentSession_StandardSignatureSkips covers the branch where
// the signature is too short to carry an embedded session key id (<97
// bytes), which must bypass the validator entirely.
func TestValidateAgentSession_StandardSignatureSkips(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0x01"), 0)
	op.Signature = make([]byte, 65) // standard ECDSA length

	fv := &fakeSessionValidator{}
	if err := v.ValidateAgentSession(op, fv); err != nil {
		t.Fatalf("ValidateAgentSession() error = %v, want nil", err)
	}
	if fv.called {
		t.Fatal("ValidateSessionKey should not be called for a standard-length signature")
	}
}

// TestValidateAgentSession_ExtractsKeyIDAndTarget covers the full extraction
// path: keyID from the first 32 signature bytes, target address and method
// selector from CallData.
func TestValidateAgentSession_ExtractsKeyIDAndTarget(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0x01"), 0)

	sig := make([]byte, 97)
	for i := range sig[:32] {
		sig[i] = byte(i + 1)
	}
	op.Signature = sig

	target := types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	callData := make([]byte, 24)
	copy(callData[:20], target[:])
	callData[20], callData[21], callData[22], callData[23] = 0xde, 0xad, 0xbe, 0xef
	op.CallData = callData
	op.CallGasLimit = uint256.NewInt(55555)

	fv := &fakeSessionValidator{}
	if err := v.ValidateAgentSession(op, fv); err != nil {
		t.Fatalf("ValidateAgentSession() error = %v, want nil", err)
	}
	if !fv.called {
		t.Fatal("ValidateSessionKey was not called")
	}
	var wantKeyID types.Hash
	copy(wantKeyID[:], sig[:32])
	if fv.keyID != wantKeyID {
		t.Fatalf("keyID = %x, want %x", fv.keyID, wantKeyID)
	}
	if fv.target != target {
		t.Fatalf("target = %x, want %x", fv.target, target)
	}
	if len(fv.methodSig) != 4 || fv.methodSig[0] != 0xde {
		t.Fatalf("methodSig = %x, want de ad be ef", fv.methodSig)
	}
	if fv.gas != 55555 {
		t.Fatalf("gas = %d, want 55555", fv.gas)
	}
}

// TestValidateAgentSession_ShortCallData covers the branches where CallData
// is too short to contain a target address or method selector.
func TestValidateAgentSession_ShortCallData(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0x01"), 0)
	op.Signature = make([]byte, 97)
	op.CallData = []byte{0x01, 0x02} // shorter than 20 bytes

	fv := &fakeSessionValidator{}
	if err := v.ValidateAgentSession(op, fv); err != nil {
		t.Fatalf("ValidateAgentSession() error = %v, want nil", err)
	}
	if fv.target != (types.Address{}) {
		t.Fatalf("target = %x, want zero address", fv.target)
	}
	if fv.methodSig != nil {
		t.Fatalf("methodSig = %x, want nil", fv.methodSig)
	}
}

// TestValidateAgentSession_PropagatesError covers propagation of the
// validator's own error.
func TestValidateAgentSession_PropagatesError(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0x01"), 0)
	op.Signature = make([]byte, 97)

	wantErr := errors.New("session key revoked")
	fv := &fakeSessionValidator{err: wantErr}
	if err := v.ValidateAgentSession(op, fv); !errors.Is(err, wantErr) {
		t.Fatalf("ValidateAgentSession() error = %v, want %v", err, wantErr)
	}
}

// fakeStateReader is a controllable StateReader for ValidateStateful tests.
type fakeStateReader struct {
	codeSize map[types.Address]int
	nonce    map[types.Address]uint64
	balance  map[types.Address]*uint256.Int
}

func newFakeStateReader() *fakeStateReader {
	return &fakeStateReader{
		codeSize: map[types.Address]int{},
		nonce:    map[types.Address]uint64{},
		balance:  map[types.Address]*uint256.Int{},
	}
}

func (f *fakeStateReader) GetCodeSize(addr types.Address) int { return f.codeSize[addr] }
func (f *fakeStateReader) GetNonce(addr types.Address) uint64 { return f.nonce[addr] }
func (f *fakeStateReader) GetBalance(addr types.Address) *uint256.Int {
	if b, ok := f.balance[addr]; ok {
		return b
	}
	return uint256.NewInt(0)
}

// TestValidateStateful_UndeployedSenderRequiresInitCode covers the branch
// requiring initCode when the sender account does not yet exist.
func TestValidateStateful_UndeployedSenderRequiresInitCode(t *testing.T) {
	v := NewValidator(DefaultConfig())
	op := makeTestOp(types.HexToAddress("0x01"), 0)
	op.InitCode = nil

	state := newFakeStateReader()
	err := v.ValidateStateful(op, state)
	if !errors.Is(err, vm.ErrAccountNotDeployed) {
		t.Fatalf("ValidateStateful() error = %v, want ErrAccountNotDeployed", err)
	}
}

// TestValidateStateful_DeployedSenderRejectsInitCode covers the opposite
// branch: an already-deployed sender must not supply initCode.
func TestValidateStateful_DeployedSenderRejectsInitCode(t *testing.T) {
	v := NewValidator(DefaultConfig())
	sender := types.HexToAddress("0x01")
	op := makeTestOp(sender, 0)
	op.InitCode = []byte{0x01, 0x02, 0x03}

	state := newFakeStateReader()
	state.codeSize[sender] = 100

	err := v.ValidateStateful(op, state)
	if err == nil {
		t.Fatal("ValidateStateful() error = nil, want error for deployed sender with initCode")
	}
}

// TestValidateStateful_InsufficientBalance covers the no-paymaster prefund
// balance check.
func TestValidateStateful_InsufficientBalance(t *testing.T) {
	v := NewValidator(DefaultConfig())
	sender := types.HexToAddress("0x01")
	op := makeTestOp(sender, 0)

	state := newFakeStateReader()
	state.nonce[sender] = 1 // sender exists
	state.balance[sender] = uint256.NewInt(1)

	err := v.ValidateStateful(op, state)
	if err == nil {
		t.Fatal("ValidateStateful() error = nil, want error for insufficient balance")
	}
}

// TestValidateStateful_PaymasterNotDeployed covers the paymaster-code-size
// guard.
func TestValidateStateful_PaymasterNotDeployed(t *testing.T) {
	v := NewValidator(DefaultConfig())
	sender := types.HexToAddress("0x01")
	op := makeTestOp(sender, 0)

	paymaster := types.HexToAddress("0x02")
	paymasterAndData := make([]byte, 20)
	copy(paymasterAndData, paymaster[:])
	op.PaymasterAndData = paymasterAndData

	state := newFakeStateReader()
	state.nonce[sender] = 1

	err := v.ValidateStateful(op, state)
	if !errors.Is(err, vm.ErrPaymasterNotDeployed) {
		t.Fatalf("ValidateStateful() error = %v, want ErrPaymasterNotDeployed", err)
	}
}

// TestValidateStateful_Success covers the all-clear path: deployed sender,
// sufficient balance, no paymaster.
func TestValidateStateful_Success(t *testing.T) {
	v := NewValidator(DefaultConfig())
	sender := types.HexToAddress("0x01")
	op := makeTestOp(sender, 0)

	state := newFakeStateReader()
	state.nonce[sender] = 1
	state.balance[sender] = uint256.MustFromDecimal("1000000000000000000")

	if err := v.ValidateStateful(op, state); err != nil {
		t.Fatalf("ValidateStateful() error = %v, want nil", err)
	}
}
