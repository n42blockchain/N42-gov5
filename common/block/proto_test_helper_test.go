// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import "google.golang.org/protobuf/proto"

// protoMarshalForTest keeps the protobuf import confined to the comparison
// tests, which are the only place the old encodings are still produced.
func protoMarshalForTest(m proto.Message) ([]byte, error) {
	return proto.Marshal(m)
}
