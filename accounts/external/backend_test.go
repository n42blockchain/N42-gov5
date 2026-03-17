package external

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

type accountRPCService struct {
	version     string
	accounts    []types.Address
	signDataRes hexutil.Bytes
	signTxRes   signTransactionResult

	lastSignDataMime string
	lastSignDataAddr types.Address
	lastSignDataData string
	lastSignTxArgs   map[string]interface{}
}

func (s *accountRPCService) Version() (string, error) {
	return s.version, nil
}

func (s *accountRPCService) List() ([]types.Address, error) {
	return s.accounts, nil
}

func (s *accountRPCService) SignData(mimeType string, addr types.Address, data string) (hexutil.Bytes, error) {
	s.lastSignDataMime = mimeType
	s.lastSignDataAddr = addr
	s.lastSignDataData = data
	return s.signDataRes, nil
}

func (s *accountRPCService) SignTransaction(args map[string]interface{}) (signTransactionResult, error) {
	s.lastSignTxArgs = args
	return s.signTxRes, nil
}

func TestExternalSignerAccountsAndContains(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	signer := newTestExternalSigner(t, &accountRPCService{
		version:  "v1.2.3",
		accounts: []types.Address{addr},
	})

	accts := signer.Accounts()
	if len(accts) != 1 {
		t.Fatalf("Accounts() len = %d, want 1", len(accts))
	}
	if accts[0].Address != addr {
		t.Fatalf("Accounts()[0].Address = %s, want %s", accts[0].Address.Hex(), addr.Hex())
	}
	if !signer.Contains(accounts.Account{Address: addr}) {
		t.Fatal("Contains() = false, want true")
	}
}

func TestExternalSignerSignDataCliqueNormalizesRecoveryID(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	service := &accountRPCService{
		signDataRes: append(make(hexutil.Bytes, 64), 27),
	}
	signer := newTestExternalSigner(t, service)

	sig, err := signer.SignData(accounts.Account{Address: addr}, accounts.MimetypeClique, []byte("payload"))
	if err != nil {
		t.Fatalf("SignData() error = %v", err)
	}
	if got := sig[64]; got != 0 {
		t.Fatalf("recovery id = %d, want 0", got)
	}
	if service.lastSignDataMime != accounts.MimetypeClique {
		t.Fatalf("mimeType = %q, want %q", service.lastSignDataMime, accounts.MimetypeClique)
	}
	if service.lastSignDataAddr != addr {
		t.Fatalf("address = %s, want %s", service.lastSignDataAddr.Hex(), addr.Hex())
	}
	if service.lastSignDataData != hexutil.Encode([]byte("payload")) {
		t.Fatalf("data = %q, want %q", service.lastSignDataData, hexutil.Encode([]byte("payload")))
	}
}

func TestExternalSignerSignTextNormalizesRecoveryID(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	service := &accountRPCService{
		signDataRes: append(make(hexutil.Bytes, 64), 28),
	}
	signer := newTestExternalSigner(t, service)

	sig, err := signer.SignText(accounts.Account{Address: addr}, []byte("hello"))
	if err != nil {
		t.Fatalf("SignText() error = %v", err)
	}
	if got := sig[64]; got != 1 {
		t.Fatalf("recovery id = %d, want 1", got)
	}
	if service.lastSignDataMime != accounts.MimetypeTextPlain {
		t.Fatalf("mimeType = %q, want %q", service.lastSignDataMime, accounts.MimetypeTextPlain)
	}
}

func TestExternalSignerSignTxDecodesRawTransaction(t *testing.T) {
	from := types.HexToAddress("0x1234567890123456789012345678901234567890")
	to := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	accessList := transaction.AccessList{{
		Address:     types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		StorageKeys: []types.Hash{types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")},
	}}
	rawTx := transaction.NewTx(&transaction.AccessListTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      7,
		GasPrice:   uint256.NewInt(5),
		Gas:        21000,
		To:         &to,
		Value:      uint256.NewInt(9),
		Data:       []byte{0x01, 0x02},
		From:       &from,
		AccessList: accessList,
	})
	encoded, err := rawTx.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	service := &accountRPCService{
		signTxRes: signTransactionResult{Raw: encoded},
	}
	signer := newTestExternalSigner(t, service)

	signed, err := signer.SignTx(accounts.Account{Address: from}, rawTx, big.NewInt(1))
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}
	if signed.Type() != transaction.AccessListTxType {
		t.Fatalf("signed.Type() = %d, want %d", signed.Type(), transaction.AccessListTxType)
	}
	if signed.Nonce() != rawTx.Nonce() {
		t.Fatalf("signed.Nonce() = %d, want %d", signed.Nonce(), rawTx.Nonce())
	}
	if got := signed.AccessList(); len(got) != len(accessList) || got[0].Address != accessList[0].Address || len(got[0].StorageKeys) != len(accessList[0].StorageKeys) || got[0].StorageKeys[0] != accessList[0].StorageKeys[0] {
		t.Fatalf("signed.AccessList() = %#v, want %#v", got, accessList)
	}
	if _, ok := service.lastSignTxArgs["accessList"]; !ok {
		t.Fatal("account_signTransaction payload missing accessList")
	}
}

func TestExternalSignerSignTxRejectsEmptyResult(t *testing.T) {
	from := types.HexToAddress("0x1234567890123456789012345678901234567890")
	signer := newTestExternalSigner(t, &accountRPCService{})

	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
		From:     &from,
	})
	_, err := signer.SignTx(accounts.Account{Address: from}, tx, big.NewInt(1))
	if err == nil || err.Error() != "external signer returned empty transaction result" {
		t.Fatalf("SignTx() error = %v, want empty transaction result", err)
	}
}

func TestExternalSignerPasswordOperationsRemainUnsupported(t *testing.T) {
	signer := &ExternalSigner{}
	if _, err := signer.SignDataWithPassphrase(accounts.Account{}, "pw", accounts.MimetypeTextPlain, nil); err == nil {
		t.Fatal("SignDataWithPassphrase() error = nil, want unsupported error")
	}
	if _, err := signer.SignTextWithPassphrase(accounts.Account{}, "pw", nil); err == nil {
		t.Fatal("SignTextWithPassphrase() error = nil, want unsupported error")
	}
	if _, err := signer.SignTxWithPassphrase(accounts.Account{}, "pw", nil, nil); err == nil {
		t.Fatal("SignTxWithPassphrase() error = nil, want unsupported error")
	}
}

func newTestExternalSigner(t *testing.T, service *accountRPCService) *ExternalSigner {
	t.Helper()

	server := jsonrpc.NewServer()
	if err := server.RegisterName("account", service); err != nil {
		t.Fatalf("RegisterName() error = %v", err)
	}
	client := jsonrpc.DialInProc(server)
	t.Cleanup(client.Close)

	return &ExternalSigner{
		client:   client,
		endpoint: "inproc://clef",
		status:   "ok [version=test]",
	}
}
