package commitment

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

type mapSource map[qmdb.Hash][]byte

func (m mapSource) Get(k qmdb.Hash) ([]byte, bool) { v, ok := m[k]; return v, ok }

type fakePlain struct {
	acc  map[types.Address]*account.StateAccount
	slot map[types.Hash][]byte
}

func (f *fakePlain) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	return f.acc[a], nil
}
func (f *fakePlain) ReadAccountStorage(a types.Address, k *types.Hash) ([]byte, error) {
	return f.slot[*k], nil
}
func (f *fakePlain) ReadAccountCode(types.Address, types.Hash) ([]byte, error)  { return nil, nil }
func (f *fakePlain) ReadAccountCodeSize(types.Address, types.Hash) (int, error) { return 0, nil }

func mkAccount(nonce uint64, bal uint64) *account.StateAccount {
	a := &account.StateAccount{}
	a.Reset()
	a.Nonce = nonce
	a.Balance = *uint256.NewInt(bal)
	return a
}

func TestParseQMDBReadMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want QMDBReadMode
	}{
		{"", QMDBReadOff}, {"0", QMDBReadOff}, {"off", QMDBReadOff}, {"false", QMDBReadOff},
		{"verify", QMDBReadVerify}, {"VERIFY", QMDBReadVerify},
		{"1", QMDBReadOn}, {"true", QMDBReadOn}, {"on", QMDBReadOn}, {"yes", QMDBReadOn},
	} {
		if got := parseQMDBReadMode(tc.in); got != tc.want {
			t.Errorf("parseQMDBReadMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The default has to be OFF: this changes where consensus-critical state is
// read from, and a build that enabled it silently would change every node.
func TestQMDBReadDefaultsOff(t *testing.T) {
	if parseQMDBReadMode("") != QMDBReadOff {
		t.Fatal("QMDB state reads must default to off")
	}
}

// The whole point of the reader: the bytes QMDB holds decode to the same
// account the plain table would return, because both are account.MarshalV2().
func TestQMDBAccountMatchesPlainEncoding(t *testing.T) {
	addr := types.Address{0xAB, 0xCD}
	want := mkAccount(7, 12345)
	src := mapSource{qmdb.Hash(AccountKeyHash(addr)): EncodeAccountValue(want)}
	plain := &fakePlain{acc: map[types.Address]*account.StateAccount{addr: want}}

	r := NewQMDBStateReader(src, plain, QMDBReadOn)
	got, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatalf("ReadAccountData: %v", err)
	}
	if got == nil || got.Nonce != want.Nonce || !got.Balance.Eq(&want.Balance) {
		t.Fatalf("QMDB read = %+v, want nonce=%d balance=%s", got, want.Nonce, want.Balance.String())
	}
}

// A missing key must read as "no account", not as an empty one.
func TestQMDBMissingAccountIsNil(t *testing.T) {
	r := NewQMDBStateReader(mapSource{}, &fakePlain{acc: map[types.Address]*account.StateAccount{}}, QMDBReadOn)
	got, err := r.ReadAccountData(types.Address{0x01})
	if err != nil || got != nil {
		t.Fatalf("missing account = (%v, %v), want (nil, nil)", got, err)
	}
}

// QMDB stores slots zero-padded to 32 bytes and the plain table stores them
// trimmed. Without the trim every non-full-width slot would read as a
// divergence that is not one.
func TestQMDBStorageTrimsToPlainRepresentation(t *testing.T) {
	addr := types.Address{0x11}
	slot := types.Hash{0x22}
	padded := make([]byte, 32)
	padded[31] = 0x2a // 42
	src := mapSource{qmdb.Hash(StorageKeyHash(addr, slot)): padded}
	plain := &fakePlain{slot: map[types.Hash][]byte{slot: {0x2a}}}

	r := NewQMDBStateReader(src, plain, QMDBReadOn)
	got, err := r.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatalf("ReadAccountStorage: %v", err)
	}
	if len(got) != 1 || got[0] != 0x2a {
		t.Fatalf("QMDB slot = %x, want 2a (plain representation)", got)
	}

	rv := NewQMDBStateReader(src, plain, QMDBReadVerify)
	if _, err := rv.ReadAccountStorage(addr, &slot); err != nil {
		t.Fatalf("verify read: %v", err)
	}
	if _, sm, _ := rv.Mismatches(); sm != 0 {
		t.Fatalf("padded/trimmed pair reported %d storage mismatches, want 0", sm)
	}
}

// Verify mode must answer from the PLAIN reader, so a divergence cannot change
// a block while it is being measured -- and it must still count the divergence.
func TestVerifyModeAnswersFromPlainAndCounts(t *testing.T) {
	addr := types.Address{0x33}
	plainAcc := mkAccount(1, 100)
	qmdbAcc := mkAccount(9, 900)
	src := mapSource{qmdb.Hash(AccountKeyHash(addr)): EncodeAccountValue(qmdbAcc)}
	plain := &fakePlain{acc: map[types.Address]*account.StateAccount{addr: plainAcc}}

	r := NewQMDBStateReader(src, plain, QMDBReadVerify)
	got, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatalf("ReadAccountData: %v", err)
	}
	if got.Nonce != 1 {
		t.Fatalf("verify mode answered nonce=%d, want the PLAIN value 1", got.Nonce)
	}
	am, _, compared := r.Mismatches()
	if am != 1 || compared != 1 {
		t.Fatalf("mismatches=(%d, compared %d), want (1, 1)", am, compared)
	}
}

// Off mode must not touch QMDB at all.
func TestOffModeDelegates(t *testing.T) {
	addr := types.Address{0x44}
	plain := &fakePlain{acc: map[types.Address]*account.StateAccount{addr: mkAccount(5, 50)}}
	r := NewQMDBStateReader(mapSource{}, plain, QMDBReadOff)
	got, _ := r.ReadAccountData(addr)
	if got == nil || got.Nonce != 5 {
		t.Fatalf("off mode = %+v, want the plain account", got)
	}
	if _, _, compared := r.Mismatches(); compared != 0 {
		t.Fatalf("off mode compared %d reads, want 0", compared)
	}
}
