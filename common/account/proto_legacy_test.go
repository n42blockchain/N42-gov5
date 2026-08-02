// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package account

import "google.golang.org/protobuf/proto"

// marshalProtoLegacy produces the protobuf account encoding.
//
// No production path writes this form any more -- MarshalV2 is the storage
// encoding, and StateAccount.Unmarshal survives only to decode records written
// before that. Keeping the producer here, in the tests, means the legacy
// decoder stays covered without the tree carrying a writer nothing calls.
func marshalProtoLegacy(a *StateAccount) ([]byte, error) {
	return proto.Marshal(a.ToProtoMessage())
}
