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

package tests

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/n42blockchain/N42/conf"
)

//go:embed  allocs
var allocs embed.FS

func ReadGenesis(filename string) *conf.Genesis {
	f, err := allocs.Open(filename)
	if err != nil {
		panic(filename + " not found, use default genesis")
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	gc := new(conf.Genesis)
	err = decoder.Decode(gc)
	if err != nil {
		panic(fmt.Sprintf("Could not parse genesis preallocation for %s: %v", filename, err))
	}
	return gc
}
