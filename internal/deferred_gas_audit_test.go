package internal

import (
 "strings"
 "testing"
 "github.com/holiman/uint256"
 "github.com/n42blockchain/N42/common/block"
 "github.com/n42blockchain/N42/common/transaction"
 "github.com/n42blockchain/N42/common/types"
 "github.com/n42blockchain/N42/crypto"
)

func TestAuditDeferredPlanRejectsGasOverflow(t *testing.T) {
 key,err:=crypto.GenerateKey();if err!=nil {t.Fatal(err)}
 chainID,_:=uint256.FromBig(senderRecoveryChainID)
 to:=types.Address{19:1}
 var txs []*transaction.Transaction
 for i,gas:=range []uint64{21000,^uint64(0)} {
  tx,err:=transaction.SignNewTx(key,transaction.NewLondonSigner(senderRecoveryChainID),&transaction.DynamicFeeTx{ChainID:chainID,Nonce:uint64(i),GasTipCap:uint256.NewInt(1),GasFeeCap:uint256.NewInt(1_000_000_000),Gas:gas,To:&to,Value:uint256.NewInt(0)})
  if err!=nil {t.Fatal(err)};txs=append(txs,tx)
 }
 hdr:=&block.Header{Number:uint256.NewInt(10),Time:1,GasLimit:^uint64(0),BaseFee:uint256.NewInt(1_000_000_000)}
 if _,err:=deferredTxPlan(deferredTestConfig(),hdr,txs);err==nil || !strings.Contains(err.Error(),"gas limit") {t.Fatalf("gas overflow accepted: %v",err)}
}
