//go:build ignore

package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/crypto"
)

func main() {
	k, _ := hex.DecodeString("46421e0087590765d2eba920834caa9ada08e30bb9aa0e6f54b16a3f57a5630a")
	key, _ := crypto.ToECDSA(k)
	os.MkdirAll("D:/N42/mpt-full/keystore", 0755)
	ks := keystore.NewKeyStore("D:/N42/mpt-full/keystore", keystore.StandardScryptN, keystore.StandardScryptP)
	a, err := ks.ImportECDSA(key, "test")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("OK:", a.Address.Hex())

	os.WriteFile("D:/N42/mpt-full/password.txt", []byte("test"), 0644)
	fmt.Println("password.txt written")
}
