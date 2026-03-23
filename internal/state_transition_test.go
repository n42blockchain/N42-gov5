package internal

import (
	"crypto/ecdsa"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type testSetCodeMessage struct {
	from       types.Address
	to         *types.Address
	gasPrice   *uint256.Int
	feeCap     *uint256.Int
	tip        *uint256.Int
	blobFeeCap *uint256.Int
	blobHashes []types.Hash
	gas        uint64
	value      *uint256.Int
	nonce      uint64
	data       []byte
	accessList transaction.AccessList
	authList   transaction.AuthorizationList
	checkNonce bool
	isFree     bool
}

func (m testSetCodeMessage) From() types.Address                     { return m.from }
func (m testSetCodeMessage) To() *types.Address                      { return m.to }
func (m testSetCodeMessage) GasPrice() *uint256.Int                  { return m.gasPrice }
func (m testSetCodeMessage) FeeCap() *uint256.Int                    { return m.feeCap }
func (m testSetCodeMessage) Tip() *uint256.Int                       { return m.tip }
func (m testSetCodeMessage) BlobFeeCap() *uint256.Int                { return m.blobFeeCap }
func (m testSetCodeMessage) BlobHashes() []types.Hash                { return m.blobHashes }
func (m testSetCodeMessage) Gas() uint64                             { return m.gas }
func (m testSetCodeMessage) Value() *uint256.Int                     { return m.value }
func (m testSetCodeMessage) Nonce() uint64                           { return m.nonce }
func (m testSetCodeMessage) CheckNonce() bool                        { return m.checkNonce }
func (m testSetCodeMessage) Data() []byte                            { return m.data }
func (m testSetCodeMessage) AccessList() transaction.AccessList      { return m.accessList }
func (m testSetCodeMessage) AuthList() transaction.AuthorizationList { return m.authList }
func (m testSetCodeMessage) IsFree() bool                            { return m.isFree }

func testStateTransitionChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        new(big.Int),
		TangerineWhistleBlock: new(big.Int),
		SpuriousDragonBlock:   new(big.Int),
		ByzantiumBlock:        new(big.Int),
		ConstantinopleBlock:   new(big.Int),
		PetersburgBlock:       new(big.Int),
		IstanbulBlock:         new(big.Int),
		MuirGlacierBlock:      new(big.Int),
		BerlinBlock:           new(big.Int),
		LondonBlock:           new(big.Int),
		ArrowGlacierBlock:     new(big.Int),
		GrayGlacierBlock:      new(big.Int),
		ShanghaiBlock:         new(big.Int),
		CancunBlock:           new(big.Int),
		PragueTime:            new(big.Int),
		OsakaTime:             big.NewInt(1),
	}
}

func signAuthorization(t *testing.T, key *ecdsa.PrivateKey, auth *transaction.Authorization) *transaction.Authorization {
	t.Helper()

	hash := auth.SigningHash()
	sig, err := crypto.Sign(hash[:], key)
	if err != nil {
		t.Fatalf("sign authorization: %v", err)
	}
	auth.R = uint256.NewInt(0).SetBytes(sig[:32])
	auth.S = uint256.NewInt(0).SetBytes(sig[32:64])
	auth.V = uint256.NewInt(uint64(sig[64]))
	return auth
}

func storageSlot(i byte) types.Hash {
	var slot types.Hash
	slot[31] = i
	return slot
}

func pushUint64(code []byte, value uint64) []byte {
	if value == 0 {
		return append(code, byte(vm2.PUSH0))
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	i := 0
	for i < len(buf) && buf[i] == 0 {
		i++
	}
	n := len(buf) - i
	code = append(code, byte(vm2.PUSH1)+byte(n-1))
	return append(code, buf[i:]...)
}

func buildStateTransitionModExpInput(base, exponent, modulus []byte) []byte {
	input := make([]byte, 96+len(base)+len(exponent)+len(modulus))
	binary.BigEndian.PutUint64(input[24:32], uint64(len(base)))
	binary.BigEndian.PutUint64(input[56:64], uint64(len(exponent)))
	binary.BigEndian.PutUint64(input[88:96], uint64(len(modulus)))
	copy(input[96:], base)
	copy(input[96+len(base):], exponent)
	copy(input[96+len(base)+len(exponent):], modulus)
	return input
}

func buildModExpGasCapWrapper(requestedGas uint64) []byte {
	code := make([]byte, 0, 64)
	code = append(code, byte(vm2.CALLDATASIZE), byte(vm2.PUSH0), byte(vm2.PUSH0), byte(vm2.CALLDATACOPY))
	code = append(code, byte(vm2.PUSH0), byte(vm2.PUSH0), byte(vm2.CALLDATASIZE), byte(vm2.PUSH0), byte(vm2.PUSH0))
	code = append(code, byte(vm2.PUSH1), 0x05)
	code = pushUint64(code, requestedGas)
	code = append(code, byte(vm2.CALL))
	code = append(code, byte(vm2.PUSH0), byte(vm2.SSTORE))
	code = append(code, byte(vm2.GAS), byte(vm2.PUSH1), 0x01, byte(vm2.SSTORE), byte(vm2.STOP))
	return code
}

func buildBlobFeeSubtractionContractCode() []byte {
	return []byte{
		byte(vm2.ORIGIN), byte(vm2.BALANCE), byte(vm2.PUSH0), byte(vm2.SSTORE),
		byte(vm2.PUSH0), byte(vm2.PUSH0), byte(vm2.PUSH0), byte(vm2.PUSH0),
		byte(vm2.CALLVALUE), byte(vm2.SELFBALANCE), byte(vm2.SUB),
		byte(vm2.ORIGIN), byte(vm2.GAS), byte(vm2.CALL),
		byte(vm2.ORIGIN), byte(vm2.BALANCE), byte(vm2.PUSH1), 0x01, byte(vm2.SSTORE),
		byte(vm2.STOP),
	}
}

func TestApplyMessageExecutesDelegatedCodeForAuthorizedEOA(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	authKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate auth key: %v", err)
	}

	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authSigner := crypto.PubkeyToAddress(authKey.PublicKey)
	target := types.HexToAddress("0x3d8e2d77bca8c0ed68f6d4860444bad2cc2cd661")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	ibs.CreateAccount(sender, false)
	ibs.AddBalance(sender, uint256.NewInt(10_000_000_000_000_000))
	ibs.CreateAccount(authSigner, false)
	ibs.AddBalance(authSigner, uint256.NewInt(1))
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, types.Hex2Bytes("60011e60005560021e6001557001000000000000000000000000000000001e6002557f80000000000000000000000000000000000000000000000000000000000000001e60035500"))

	auth := signAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: target,
		Nonce:   0,
	})

	to := authSigner
	msg := testSetCodeMessage{
		from:     sender,
		to:       &to,
		gasPrice: uint256.NewInt(7),
		feeCap:   uint256.NewInt(7),
		tip:      uint256.NewInt(0),
		gas:      200_000,
		value:    uint256.NewInt(0),
		authList: transaction.AuthorizationList{auth},
	}

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       1,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	config := testStateTransitionChainConfig()
	blockCtx := NewEVMBlockContext(header, func(uint64) types.Hash { return types.Hash{} }, nil, &coinbase)
	txCtx := evmtypes.TxContext{Origin: msg.From(), GasPrice: msg.GasPrice()}
	evm := vm2.NewEVM(blockCtx, txCtx, ibs, config, vm2.Config{})
	gp := new(common.GasPool).AddGas(header.GasLimit)

	result, err := ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		t.Fatalf("ApplyMessage error: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("ApplyMessage execution error: %v (usedGas=%d)", result.Err, result.UsedGas)
	}
	if result.UsedGas != 102_044 {
		t.Fatalf("ApplyMessage usedGas = %d, want 102044", result.UsedGas)
	}

	want := []uint64{255, 254, 127, 0}
	for i, expected := range want {
		slot := storageSlot(byte(i))
		var got uint256.Int
		ibs.GetState(authSigner, &slot, &got)
		if got.Uint64() != expected {
			t.Fatalf("slot %d = %d, want %d", i, got.Uint64(), expected)
		}
	}
	if nonce := ibs.GetNonce(authSigner); nonce != 1 {
		t.Fatalf("auth signer nonce = %d, want 1", nonce)
	}
	if nonce := ibs.GetNonce(sender); nonce != 1 {
		t.Fatalf("sender nonce = %d, want 1", nonce)
	}
}

func TestApplyMessageClearsDelegationWhenAuthorizationResets(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	authKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate auth key: %v", err)
	}

	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authSigner := crypto.PubkeyToAddress(authKey.PublicKey)
	target := types.HexToAddress("0x3d8e2d77bca8c0ed68f6d4860444bad2cc2cd661")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	ibs.CreateAccount(sender, false)
	ibs.AddBalance(sender, uint256.NewInt(10_000_000_000_000_000))
	ibs.CreateAccount(authSigner, false)
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, types.Hex2Bytes("600160005500"))

	authSet := signAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: target,
		Nonce:   0,
	})
	authReset := signAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: types.Address{},
		Nonce:   1,
	})

	to := authSigner
	msg := testSetCodeMessage{
		from:     sender,
		to:       &to,
		gasPrice: uint256.NewInt(7),
		feeCap:   uint256.NewInt(7),
		tip:      uint256.NewInt(0),
		gas:      200_000,
		value:    uint256.NewInt(0),
		authList: transaction.AuthorizationList{authSet, authReset},
	}

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       1,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	config := testStateTransitionChainConfig()
	blockCtx := NewEVMBlockContext(header, func(uint64) types.Hash { return types.Hash{} }, nil, &coinbase)
	txCtx := evmtypes.TxContext{Origin: msg.From(), GasPrice: msg.GasPrice()}
	evm := vm2.NewEVM(blockCtx, txCtx, ibs, config, vm2.Config{})
	gp := new(common.GasPool).AddGas(header.GasLimit)

	result, err := ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		t.Fatalf("ApplyMessage error: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("ApplyMessage execution error: %v (usedGas=%d)", result.Err, result.UsedGas)
	}
	if nonce := ibs.GetNonce(authSigner); nonce != 2 {
		t.Fatalf("auth signer nonce = %d, want 2", nonce)
	}
	if code := ibs.GetCode(authSigner); len(code) != 0 {
		t.Fatalf("auth signer code len = %d, want 0", len(code))
	}
}

func TestApplyTransactionExecutesDecodedSetCodeTx(t *testing.T) {
	db := memdb.NewTestDB(t)
	txdb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txdb, 1))

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	authKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate auth key: %v", err)
	}

	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authSigner := crypto.PubkeyToAddress(authKey.PublicKey)
	target := types.HexToAddress("0x3d8e2d77bca8c0ed68f6d4860444bad2cc2cd661")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	ibs.CreateAccount(sender, false)
	ibs.AddBalance(sender, uint256.NewInt(10_000_000_000_000_000))
	ibs.CreateAccount(authSigner, false)
	ibs.AddBalance(authSigner, uint256.NewInt(1))
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, types.Hex2Bytes("60011e60005560021e6001557001000000000000000000000000000000001e6002557f80000000000000000000000000000000000000000000000000000000000000001e60035500"))

	auth := signAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: target,
		Nonce:   0,
	})

	rawTx := transaction.NewTx(&transaction.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(7),
		Gas:       200_000,
		To:        &authSigner,
		Value:     uint256.NewInt(0),
		AuthList:  transaction.AuthorizationList{auth},
	})
	signer := transaction.NewLondonSigner(big.NewInt(1))
	signedTx, err := transaction.SignTx(rawTx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	encoded, err := transaction.EncodeEthereumTransaction(signedTx)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction: %v", err)
	}
	decodedTx, err := transaction.DecodeEthereumTransaction(encoded)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction: %v", err)
	}

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       1,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	blockHashFunc := func(uint64) types.Hash { return types.Hash{} }
	gp := new(common.GasPool).AddGas(header.GasLimit)
	usedGas := uint64(0)

	receipt, _, err := ApplyTransaction(
		testStateTransitionChainConfig(),
		blockHashFunc,
		nil,
		&coinbase,
		gp,
		ibs,
		state.NewNoopWriter(),
		header,
		decodedTx,
		&usedGas,
		vm2.Config{},
	)
	if err != nil {
		t.Fatalf("ApplyTransaction error: %v", err)
	}
	if receipt == nil {
		t.Fatal("ApplyTransaction returned nil receipt")
	}
	if receipt.GasUsed != 102_044 {
		t.Fatalf("receipt.GasUsed = %d, want 102044", receipt.GasUsed)
	}

	want := []uint64{255, 254, 127, 0}
	for i, expected := range want {
		slot := storageSlot(byte(i))
		var got uint256.Int
		ibs.GetState(authSigner, &slot, &got)
		if got.Uint64() != expected {
			t.Fatalf("slot %d = %d, want %d", i, got.Uint64(), expected)
		}
	}
}

func TestApplyMessageSubtractsBlobFeeBeforeExecution(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	sender := types.HexToAddress("0x1000000000000000000000000000000000000001")
	contract := types.HexToAddress("0x2000000000000000000000000000000000000002")
	coinbase := types.HexToAddress("0x3000000000000000000000000000000000000003")

	ibs.CreateAccount(sender, false)
	ibs.CreateAccount(contract, true)
	ibs.SetCode(contract, buildBlobFeeSubtractionContractCode())
	ibs.AddBalance(contract, uint256.NewInt(100))

	msg := testSetCodeMessage{
		from:       sender,
		to:         &contract,
		gasPrice:   uint256.NewInt(7),
		feeCap:     uint256.NewInt(7),
		tip:        uint256.NewInt(0),
		blobFeeCap: uint256.NewInt(1),
		blobHashes: []types.Hash{{1}},
		gas:        500_000,
		value:      uint256.NewInt(0),
	}

	gasCost := new(uint256.Int).Mul(uint256.NewInt(msg.gas), msg.gasPrice)
	blobCost := new(uint256.Int).Mul(uint256.NewInt(params.BlobTxBlobGasPerBlob), uint256.NewInt(1))
	ibs.AddBalance(sender, new(uint256.Int).Add(gasCost, blobCost))

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       1,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	config := testStateTransitionChainConfig()
	blockCtx := NewEVMBlockContext(header, func(uint64) types.Hash { return types.Hash{} }, nil, &coinbase)
	txCtx := evmtypes.TxContext{Origin: msg.From(), GasPrice: msg.GasPrice()}
	evm := vm2.NewEVM(blockCtx, txCtx, ibs, config, vm2.Config{})
	gp := new(common.GasPool).AddGas(header.GasLimit)

	result, err := ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		t.Fatalf("ApplyMessage error: %v", err)
	}
	if result.Err != nil {
		t.Fatalf("ApplyMessage execution error: %v", result.Err)
	}

	var slot0, slot1 uint256.Int
	key0 := storageSlot(0)
	key1 := storageSlot(1)
	ibs.GetState(contract, &key0, &slot0)
	ibs.GetState(contract, &key1, &slot1)
	if slot0.Uint64() != 0 {
		t.Fatalf("slot 0 = %d, want 0", slot0.Uint64())
	}
	if slot1.Uint64() != 100 {
		t.Fatalf("slot 1 = %d, want 100", slot1.Uint64())
	}
}

func TestApplyTransactionOsakaModExpGasCapWrapper(t *testing.T) {
	db := memdb.NewTestDB(t)
	txdb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txdb, 1))

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")
	contractAddr := types.HexToAddress("0x2222222222222222222222222222222222222222")

	ibs.CreateAccount(sender, false)
	ibs.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000_000))
	ibs.CreateAccount(contractAddr, true)
	ibs.SetCode(contractAddr, buildModExpGasCapWrapper(520_060_928))

	modexpInput := buildStateTransitionModExpInput(
		make([]byte, 32),
		make([]byte, 1024),
		bytesRepeat(0x01, 1024),
	)
	rules := testStateTransitionChainConfig().RulesWithTimestamp(1, 1)
	modexp := vm2.GetPrecompiledContract(types.BytesToAddress([]byte{5}), rules)
	if modexp == nil {
		t.Fatal("osaka modexp precompile not found")
	}
	requiredGas := modexp.RequiredGas(modexpInput)
	if requiredGas <= fixedgas.MaxTxnGasLimit {
		t.Fatalf("requiredGas = %d, want > tx gas limit cap %d", requiredGas, fixedgas.MaxTxnGasLimit)
	}

	rawTx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(7),
		Gas:      fixedgas.MaxTxnGasLimit,
		To:       &contractAddr,
		Value:    uint256.NewInt(0),
		Data:     modexpInput,
	})
	signer := transaction.NewEIP155Signer(big.NewInt(1))
	signedTx, err := transaction.SignTx(rawTx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	encoded, err := transaction.EncodeEthereumTransaction(signedTx)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction: %v", err)
	}
	decodedTx, err := transaction.DecodeEthereumTransaction(encoded)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction: %v", err)
	}

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       1,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	gp := new(common.GasPool).AddGas(header.GasLimit)
	usedGas := uint64(0)

	receipt, _, err := ApplyTransaction(
		testStateTransitionChainConfig(),
		func(uint64) types.Hash { return types.Hash{} },
		nil,
		&coinbase,
		gp,
		ibs,
		state.NewNoopWriter(),
		header,
		decodedTx,
		&usedGas,
		vm2.Config{},
	)
	if err != nil {
		t.Fatalf("ApplyTransaction error: %v", err)
	}
	if receipt == nil {
		t.Fatal("ApplyTransaction returned nil receipt")
	}

	var callResult uint256.Int
	var gasLeft uint256.Int
	slot0 := storageSlot(0)
	slot1 := storageSlot(1)
	ibs.GetState(contractAddr, &slot0, &callResult)
	ibs.GetState(contractAddr, &slot1, &gasLeft)

	if callResult.Uint64() != 0 {
		t.Fatalf("call result = %d, want 0 (CALL should fail under tx gas cap)", callResult.Uint64())
	}
	if receipt.GasUsed <= 16_000_000 {
		t.Fatalf("receipt.GasUsed = %d, want > 16000000 to confirm gas-cap failure path", receipt.GasUsed)
	}
	if gasLeft.Uint64() >= 500_000 {
		t.Fatalf("gasLeft = %d, want < 500000 after failed gas-cap call", gasLeft.Uint64())
	}
}

func TestApplyTransactionAuthorizedTxRefundsExistingAuthority(t *testing.T) {
	db := memdb.NewTestDB(t)
	txdb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txdb, 1))

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	authKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate auth key: %v", err)
	}
	recipientKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authSigner := crypto.PubkeyToAddress(authKey.PublicKey)
	recipient := crypto.PubkeyToAddress(recipientKey.PublicKey)
	target := types.HexToAddress("0x3d8e2d77bca8c0ed68f6d4860444bad2cc2cd661")
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	ibs.CreateAccount(sender, false)
	ibs.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000_000))
	ibs.CreateAccount(authSigner, false)
	ibs.AddBalance(authSigner, uint256.NewInt(1))
	ibs.CreateAccount(recipient, false)
	ibs.AddBalance(recipient, uint256.NewInt(1))
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, []byte{byte(vm2.STOP)})

	auth := signAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: target,
		Nonce:   0,
	})

	accessList := transaction.AccessList{{
		Address:     authSigner,
		StorageKeys: nil,
	}}
	authList := transaction.AuthorizationList{auth}
	intrinsicGas, err := IntrinsicGas(nil, accessList, authList, false, true, true, true, true, false)
	if err != nil {
		t.Fatalf("IntrinsicGas error: %v", err)
	}

	rawTx := transaction.NewTx(&transaction.SetCodeTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      0,
		GasTipCap:  uint256.NewInt(0),
		GasFeeCap:  uint256.NewInt(7),
		Gas:        intrinsicGas,
		To:         &recipient,
		Value:      uint256.NewInt(0),
		AccessList: accessList,
		AuthList:   authList,
	})
	signer := transaction.NewLondonSigner(big.NewInt(1))
	signedTx, err := transaction.SignTx(rawTx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	encoded, err := transaction.EncodeEthereumTransaction(signedTx)
	if err != nil {
		t.Fatalf("EncodeEthereumTransaction: %v", err)
	}
	decodedTx, err := transaction.DecodeEthereumTransaction(encoded)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction: %v", err)
	}

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       1,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
	gp := new(common.GasPool).AddGas(header.GasLimit)
	usedGas := uint64(0)

	receipt, _, err := ApplyTransaction(
		testStateTransitionChainConfig(),
		func(uint64) types.Hash { return types.Hash{} },
		nil,
		&coinbase,
		gp,
		ibs,
		state.NewNoopWriter(),
		header,
		decodedTx,
		&usedGas,
		vm2.Config{},
	)
	if err != nil {
		t.Fatalf("ApplyTransaction error: %v", err)
	}
	if receipt == nil {
		t.Fatal("ApplyTransaction returned nil receipt")
	}

	wantGasUsed := intrinsicGas - intrinsicGas/params.RefundQuotientEIP3529
	if receipt.GasUsed != wantGasUsed {
		t.Fatalf("receipt.GasUsed = %d, want %d", receipt.GasUsed, wantGasUsed)
	}
	if usedGas != wantGasUsed {
		t.Fatalf("usedGas = %d, want %d", usedGas, wantGasUsed)
	}
	if nonce := ibs.GetNonce(authSigner); nonce != 1 {
		t.Fatalf("auth signer nonce = %d, want 1", nonce)
	}
	if !vm2.HasDelegation(ibs.GetCode(authSigner)) {
		t.Fatal("auth signer code does not contain delegation prefix")
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestApplyTransactionCancunModExpCallCodeOverflowingExponentLength(t *testing.T) {
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txDb, 1))

	cfg := testStateTransitionChainConfig()
	cfg.PragueTime = nil
	cfg.OsakaTime = nil

	sender := types.HexToAddress("0xc9af978759eab5f729b72600e33db72470631d94")
	contractAddr := types.HexToAddress("0x456758a1acd59a799ba43a581241cf4de3bc5a05")
	coinbase := types.HexToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba")
	rawTx := types.FromHex1("0xf8c1800a83015f9094456758a1acd59a799ba43a581241cf4de3bc5a0580b86000000000000000000000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000026a01c3ab8bbd6afb6c57925d35628b4a43532f58589bf58556a06e929a205d24906a008b5a2bbdb188501f4d51289e37dafa7ebb5b49cfb41826dfe5a1bd56c8609d6")
	balance, err := uint256.FromHex("0x3635c9adc5dea00000")
	if err != nil {
		t.Fatalf("uint256.FromHex: %v", err)
	}

	ibs.CreateAccount(sender, false)
	ibs.AddBalance(sender, balance)
	ibs.CreateAccount(contractAddr, true)
	ibs.SetCode(contractAddr, types.FromHex1("0x36600060003760206103e8366000600060055af26001556103e85160025500"))

	decodedTx, err := transaction.DecodeEthereumTransaction(rawTx)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction: %v", err)
	}

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   10_000_000_000,
		Time:       1_000,
		BaseFee:    uint256.NewInt(10),
		Difficulty: uint256.NewInt(0),
	}
	gp := new(common.GasPool).AddGas(header.GasLimit)
	usedGas := uint64(0)
	zeroBeaconRoot := types.Hash{}
	if err := ProcessBeaconBlockRoot(&zeroBeaconRoot, cfg, ibs, header, nil); err != nil {
		t.Fatalf("ProcessBeaconBlockRoot: %v", err)
	}
	ibs.Prepare(decodedTx.Hash(), types.Hash{}, 0)

	receipt, _, err := ApplyTransaction(
		cfg,
		func(uint64) types.Hash { return types.Hash{} },
		nil,
		&coinbase,
		gp,
		ibs,
		state.NewNoopWriter(),
		header,
		decodedTx,
		&usedGas,
		vm2.Config{},
	)
	if err != nil {
		t.Fatalf("ApplyTransaction error: %v", err)
	}
	if receipt == nil {
		t.Fatal("ApplyTransaction returned nil receipt")
	}
	if receipt.Status != block.ReceiptStatusSuccessful {
		t.Fatalf("receipt status = %d, want %d", receipt.Status, block.ReceiptStatusSuccessful)
	}
	if receipt.GasUsed != 46_148 {
		t.Fatalf("receipt.GasUsed = %d, want 46148", receipt.GasUsed)
	}
	if usedGas != 46_148 {
		t.Fatalf("usedGas = %d, want 46148", usedGas)
	}
	if nonce := ibs.GetNonce(sender); nonce != 1 {
		t.Fatalf("sender nonce = %d, want 1", nonce)
	}

	slot1 := storageSlot(1)
	var slot1Value uint256.Int
	ibs.GetState(contractAddr, &slot1, &slot1Value)
	if got := slot1Value.Uint64(); got != 1 {
		t.Fatalf("storage[1] = %d, want 1", got)
	}
}
