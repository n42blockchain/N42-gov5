package mdbx

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
)

func TestPutNoOverwriteRejectsAutoDupSortConversion(t *testing.T) {
	cursor := &MdbxCursor{
		bucketCfg: kv.TableCfgItem{AutoDupSortKeysConversion: true},
	}

	err := cursor.PutNoOverwrite([]byte("k"), []byte("v"))
	if err == nil || !strings.Contains(err.Error(), "AutoDupSortKeysConversion") {
		t.Fatalf("PutNoOverwrite() error = %v, want AutoDupSortKeysConversion error", err)
	}
}
