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
// Documentation stub for the jmt/store sub-package. The in-memory
// MemStore used by unit tests is declared in the parent jmt package to
// avoid an import cycle, so this file only carries the package comment
// for store implementations that depend on heavier backends such as
// MDBX. Runtime code lives in cached_store.go, lazy_db_store.go,
// mdbx_store.go, pooled_db_store.go and store.go.

// Package store provides external NodeStore implementations.
// The in-memory MemStore used for testing lives in the parent jmt package
// to avoid import cycles. This sub-package is reserved for implementations
// with heavier dependencies (MDBX, etc.).
package store
