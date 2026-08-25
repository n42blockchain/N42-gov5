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
// Per-type decoder / writer cache. typeCache memoizes a typeinfo
// entry (decoder, writer and their construction errors) keyed by
// reflect type + struct tag so repeated encode/decode calls skip
// the reflection scan. The tags struct parses rlp:"nil",
// rlp:"optional" and rlp:"tail" annotations used by consensus and
// transaction struct definitions.

package rlp

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	// typeCacheMutex serialises generation only. Lookups do not take it.
	typeCacheMutex sync.Mutex

	// typeCache holds an immutable map, replaced wholesale under
	// typeCacheMutex whenever a new type is generated.
	//
	// It used to be a plain map behind an RWMutex, which is what geth's rlp
	// did originally. RLock never blocks a reader, but it increments the
	// mutex's reader counter, so every encode wrote to one shared cache line.
	// Encoding is on the hot path of receipt-root derivation - once per
	// receipt per block - so with dozens of cores replaying blocks in parallel
	// that line ping-pongs continuously between cores. On a 104-worker witness
	// replay, sync/atomic.(*Int32).Add alone was 2.3% of all CPU and 71% of
	// every RLock in the process came from here.
	//
	// Reads are now a single atomic load of a pointer that stops changing once
	// the process has seen each type once, so the line stays shared in every
	// core's cache and costs nothing to read.
	typeCache atomic.Pointer[map[typekey]*typeinfo]

	// typeCacheNext is the map being built during generation. It is valid only
	// while typeCacheMutex is held.
	typeCacheNext map[typekey]*typeinfo
)

func init() {
	empty := make(map[typekey]*typeinfo)
	typeCache.Store(&empty)
}

type typeinfo struct {
	decoder    decoder
	decoderErr error // error from makeDecoder
	writer     writer
	writerErr  error // error from makeWriter
}

// tags represents struct tags.
type tags struct {
	// rlp:"nil" controls whether empty input results in a nil pointer.
	// nilKind is the kind of empty value allowed for the field.
	nilKind Kind
	nilOK   bool

	// rlp:"optional" allows for a field to be missing in the input list.
	// If this is set, all subsequent fields must also be optional.
	optional bool

	// rlp:"tail" controls whether this field swallows additional list elements. It can
	// only be set for the last field, which must be of slice type.
	tail bool

	// rlp:"-" ignores fields.
	ignored bool
}

// typekey is the key of a type in typeCache. It includes the struct tags because
// they might generate a different decoder.
type typekey struct {
	reflect.Type
	tags
}

type decoder func(*Stream, reflect.Value) error

type writer func(reflect.Value, *encbuf) error

func cachedDecoder(typ reflect.Type) (decoder, error) {
	info := cachedTypeInfo(typ, tags{})
	return info.decoder, info.decoderErr
}

func cachedWriter(typ reflect.Type) (writer, error) {
	info := cachedTypeInfo(typ, tags{})
	return info.writer, info.writerErr
}

func cachedTypeInfo(typ reflect.Type, tags tags) *typeinfo {
	if info := (*typeCache.Load())[typekey{typ, tags}]; info != nil {
		return info
	}
	// Not in the cache, need to generate info for this type.
	typeCacheMutex.Lock()
	defer typeCacheMutex.Unlock()

	cur := *typeCache.Load()
	if info := cur[typekey{typ, tags}]; info != nil {
		// Another goroutine generated it while we waited for the lock.
		return info
	}
	// Build the replacement map, generate into it, then publish. Readers keep
	// using the old map until the store, so they never observe a half-built
	// entry - which matters because generation deliberately publishes a dummy
	// first, see cachedTypeInfo1.
	typeCacheNext = make(map[typekey]*typeinfo, len(cur)+1)
	for k, v := range cur {
		typeCacheNext[k] = v
	}
	info := cachedTypeInfo1(typ, tags)
	next := typeCacheNext
	typeCacheNext = nil
	typeCache.Store(&next)
	return info
}

// cachedTypeInfo1 resolves a type while generation is in progress. The caller
// must hold typeCacheMutex and have populated typeCacheNext; recursive lookups
// from generate() land here so a type that refers to itself resolves to the
// dummy rather than recursing forever.
func cachedTypeInfo1(typ reflect.Type, tags tags) *typeinfo {
	key := typekey{typ, tags}
	info := typeCacheNext[key]
	if info != nil {
		return info
	}
	// Put a dummy value into the pending map before generating. If the
	// generator tries to look itself up, it gets the dummy and won't call
	// itself recursively.
	info = new(typeinfo)
	typeCacheNext[key] = info
	info.generate(typ, tags)
	return info
}

type field struct {
	index    int
	info     *typeinfo
	optional bool
}

// structFields resolves the typeinfo of all public fields in a struct type.
func structFields(typ reflect.Type) (fields []field, err error) {
	var (
		lastPublic  = lastPublicField(typ)
		anyOptional = false
	)
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); f.PkgPath == "" { // exported
			tags, err := parseStructTag(typ, i, lastPublic)
			if err != nil {
				return nil, err
			}

			// Skip rlp:"-" fields.
			if tags.ignored {
				continue
			}
			// If any field has the "optional" tag, subsequent fields must also have it.
			if tags.optional || tags.tail {
				anyOptional = true
			} else if anyOptional {
				return nil, fmt.Errorf(`rlp: struct field %v.%s needs "optional" tag`, typ, f.Name)
			}
			info := cachedTypeInfo1(f.Type, tags)
			fields = append(fields, field{i, info, tags.optional})
		}
	}
	return fields, nil
}

// anyOptionalFields returns the index of the first field with "optional" tag.
func firstOptionalField(fields []field) int {
	for i, f := range fields {
		if f.optional {
			return i
		}
	}
	return len(fields)
}

type structFieldError struct {
	typ   reflect.Type
	field int
	err   error
}

func (e structFieldError) Error() string {
	return fmt.Sprintf("%v (struct field %v.%s)", e.err, e.typ, e.typ.Field(e.field).Name)
}

type structTagError struct {
	typ             reflect.Type
	field, tag, err string
}

func (e structTagError) Error() string {
	return fmt.Sprintf("rlp: invalid struct tag %q for %v.%s (%s)", e.tag, e.typ, e.field, e.err)
}

func parseStructTag(typ reflect.Type, fi, lastPublic int) (tags, error) {
	f := typ.Field(fi)
	var ts tags
	for _, t := range strings.Split(f.Tag.Get("rlp"), ",") {
		switch t = strings.TrimSpace(t); t {
		case "":
		case "-":
			ts.ignored = true
		case "nil", "nilString", "nilList":
			ts.nilOK = true
			if f.Type.Kind() != reflect.Ptr {
				return ts, structTagError{typ, f.Name, t, "field is not a pointer"}
			}
			switch t {
			case "nil":
				ts.nilKind = defaultNilKind(f.Type.Elem())
			case "nilString":
				ts.nilKind = String
			case "nilList":
				ts.nilKind = List
			}
		case "optional":
			ts.optional = true
			if ts.tail {
				return ts, structTagError{typ, f.Name, t, `also has "tail" tag`}
			}
		case "tail":
			ts.tail = true
			if fi != lastPublic {
				return ts, structTagError{typ, f.Name, t, "must be on last field"}
			}
			if ts.optional {
				return ts, structTagError{typ, f.Name, t, `also has "optional" tag`}
			}
			if f.Type.Kind() != reflect.Slice {
				return ts, structTagError{typ, f.Name, t, "field type is not slice"}
			}
		default:
			return ts, fmt.Errorf("rlp: unknown struct tag %q on %v.%s", t, typ, f.Name)
		}
	}
	return ts, nil
}

func lastPublicField(typ reflect.Type) int {
	last := 0
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).PkgPath == "" {
			last = i
		}
	}
	return last
}

func (i *typeinfo) generate(typ reflect.Type, tags tags) {
	i.decoder, i.decoderErr = makeDecoder(typ, tags)
	i.writer, i.writerErr = makeWriter(typ, tags)
}

// defaultNilKind determines whether a nil pointer to typ encodes/decodes
// as an empty string or empty list.
func defaultNilKind(typ reflect.Type) Kind {
	k := typ.Kind()
	if isUint(k) || k == reflect.String || k == reflect.Bool || isByteArray(typ) {
		return String
	}
	return List
}

func isUint(k reflect.Kind) bool {
	return k >= reflect.Uint && k <= reflect.Uintptr
}

func isByte(typ reflect.Type) bool {
	return typ.Kind() == reflect.Uint8 && !typ.Implements(encoderInterface)
}

func isByteArray(typ reflect.Type) bool {
	return (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) && isByte(typ.Elem())
}
