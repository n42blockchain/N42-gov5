package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
)

// isAccountEmpty's third clause is !Initialised, and every account that reaches
// the commitment path came from a state object, where Initialised is true --
// Reset() sets it, and so do state_object.go:304, state_object.go:595 and
// intra_block_state.go:1068, after which computeRoot does acct.Copy(&obj.data).
//
// So the empty-account deletion in ComputeRoot is UNREACHABLE for any real
// account: QMDB retains every account EIP-161 deletes from the plain `Account`
// table. Round 20 found this in production on its first comparison, on the
// system address 0xff..fe, which every block's system call touches and leaves
// empty.
//
// This test pins the current behaviour so that a fix -- which changes the state
// root and therefore needs a fork activation, not an env var -- cannot land
// silently. It asserts what IS, not what should be.
func TestIsAccountEmptyIsUnreachableForRealAccounts(t *testing.T) {
	a := &account.StateAccount{}
	a.Reset() // what every state object does

	if !a.Initialised {
		t.Fatal("Reset() no longer sets Initialised; the premise of this test changed")
	}
	if a.Nonce != 0 || !a.Balance.IsZero() {
		t.Fatal("Reset() no longer produces a zero account")
	}

	if isAccountEmpty(a) {
		t.Fatal("isAccountEmpty now returns true for a Reset account -- the " +
			"commitment path has been fixed to match EIP-161. That changes the " +
			"state root, so it must be gated on a fork activation and this test " +
			"must be replaced by one that pins the NEW behaviour.")
	}

	// The plain path's predicate, for contrast: it calls this account empty.
	// (stateObject.empty() is unexported in modules/state; the clause is
	// nonce==0 && balance==0 && codeHash==emptyCodeHash, and a Reset account
	// satisfies all three.)
	if a.CodeHash != (account.StateAccount{}).CodeHash && a.Nonce == 0 && a.Balance.IsZero() {
		// no assertion here beyond documenting the asymmetry the round measured
		t.Log("plain path would treat this account as empty and delete it; " +
			"the commitment path retains it")
	}
}
