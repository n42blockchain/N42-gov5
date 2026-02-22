// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.9

package blake2b

import (
	"crypto"
	"hash"
)

func init() {
	crypto.RegisterHash(crypto.BLAKE2b_256, func() hash.Hash { h, _ := New256(nil); return h })
	crypto.RegisterHash(crypto.BLAKE2b_384, func() hash.Hash { h, _ := New384(nil); return h })
	crypto.RegisterHash(crypto.BLAKE2b_512, func() hash.Hash { h, _ := New512(nil); return h })
}
