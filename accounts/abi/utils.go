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
//
// Small utilities shared by Method, Event and Error constructors.
// sanitizeInputs fills in arg<N> placeholders for unnamed arguments
// and builds the human-readable and canonical-type name lists.
// buildSignature joins those into "name(type1,type2,...)" form, and
// ResolveNameConflict appends a numeric suffix to disambiguate
// overloaded identifiers when binding generators hit collisions.

package abi

import (
	"fmt"
	"strings"
)

// sanitizeInputs fills in default names for unnamed arguments and builds
// string and signature representations. This logic is shared by NewEvent
// and NewError to avoid duplication.
func sanitizeInputs(inputs Arguments) (names []string, types []string) {
	names = make([]string, len(inputs))
	types = make([]string, len(inputs))
	for i, input := range inputs {
		if input.Name == "" {
			inputs[i] = Argument{
				Name:    fmt.Sprintf("arg%d", i),
				Indexed: input.Indexed,
				Type:    input.Type,
			}
		} else {
			inputs[i] = input
		}
		if input.Indexed {
			names[i] = fmt.Sprintf("%v indexed %v", input.Type, inputs[i].Name)
		} else {
			names[i] = fmt.Sprintf("%v %v", input.Type, inputs[i].Name)
		}
		types[i] = input.Type.String()
	}
	return names, types
}

// buildSignature constructs a function/event/error signature string from
// a name and a list of type strings (e.g. "foo(uint32,int256)").
func buildSignature(name string, typeStrings []string) string {
	return fmt.Sprintf("%v(%v)", name, strings.Join(typeStrings, ","))
}

// ResolveNameConflict returns the next available name for a given thing.
// This helper can be used for lots of purposes:
//
//   - In solidity function overloading is supported, this function can fix
//     the name conflicts of overloaded functions.
//   - In golang binding generation, the parameter(in function, event, error,
//     and struct definition) name will be converted to camelcase style which
//     may eventually lead to name conflicts.
//
// Name conflicts are mostly resolved by adding number suffix. e.g. if the abi contains
// Methods "send" and "send1", ResolveNameConflict would return "send2" for input "send".
func ResolveNameConflict(rawName string, used func(string) bool) string {
	name := rawName
	ok := used(name)
	for idx := 0; ok; idx++ {
		name = fmt.Sprintf("%s%d", rawName, idx)
		ok = used(name)
	}
	return name
}
