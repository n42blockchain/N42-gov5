// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package abi

import (
	"fmt"
	"reflect"
	"testing"
)

func TestParseSelector(t *testing.T) {
	mkType := func(types ...interface{}) []ArgumentMarshaling {
		var result []ArgumentMarshaling
		for i, typeOrComponents := range types {
			name := fmt.Sprintf("name%d", i)
			switch v := typeOrComponents.(type) {
			case string:
				result = append(result, ArgumentMarshaling{name, v, v, nil, false})
			case []ArgumentMarshaling:
				result = append(result, ArgumentMarshaling{name, "tuple", "tuple", v, false})
			case [][]ArgumentMarshaling:
				result = append(result, ArgumentMarshaling{name, "tuple[]", "tuple[]", v[0], false})
			default:
				t.Fatalf("unexpected type %T", typeOrComponents)
			}
		}
		return result
	}

	tests := []struct {
		input string
		name  string
		args  []ArgumentMarshaling
	}{
		{"noargs()", "noargs", []ArgumentMarshaling{}},
		{"simple(uint256,uint256,uint256)", "simple", mkType("uint256", "uint256", "uint256")},
		{"other(uint256,address)", "other", mkType("uint256", "address")},
		{"withArray(uint256[],address[2],uint8[4][][5])", "withArray", mkType("uint256[]", "address[2]", "uint8[4][][5]")},
		{"singleNest(bytes32,uint8,(uint256,uint256),address)", "singleNest", mkType("bytes32", "uint8", mkType("uint256", "uint256"), "address")},
		{"multiNest(address,(uint256[],uint256),((address,bytes32),uint256))", "multiNest",
			mkType("address", mkType("uint256[]", "uint256"), mkType(mkType("address", "bytes32"), "uint256"))},
		{"arrayNest((uint256,uint256)[],bytes32)", "arrayNest", mkType([][]ArgumentMarshaling{mkType("uint256", "uint256")}, "bytes32")},
		{"multiArrayNest((uint256,uint256)[],(uint256,uint256)[])", "multiArrayNest",
			mkType([][]ArgumentMarshaling{mkType("uint256", "uint256")}, [][]ArgumentMarshaling{mkType("uint256", "uint256")})},
		{"singleArrayNestAndArray((uint256,uint256)[],bytes32[])", "singleArrayNestAndArray",
			mkType([][]ArgumentMarshaling{mkType("uint256", "uint256")}, "bytes32[]")},
		{"singleArrayNestWithArrayAndArray((uint256[],address[2],uint8[4][][5])[],bytes32[])", "singleArrayNestWithArrayAndArray",
			mkType([][]ArgumentMarshaling{mkType("uint256[]", "address[2]", "uint8[4][][5]")}, "bytes32[]")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector, err := ParseSelector(tt.input)
			if err != nil {
				t.Fatalf("failed to parse selector '%v': %v", tt.input, err)
			}
			if selector.Name != tt.name {
				t.Errorf("unexpected function name: got %q, want %q", selector.Name, tt.name)
			}
			if selector.Type != "function" {
				t.Errorf("unexpected type: got %q, want %q", selector.Type, "function")
			}
			if !reflect.DeepEqual(selector.Inputs, tt.args) {
				t.Errorf("unexpected args: got %v, want %v", selector.Inputs, tt.args)
			}
		})
	}
}
