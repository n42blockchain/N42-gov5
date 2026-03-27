package types_pb

import ssz "github.com/prysmaticlabs/fastssz"

const (
	// Current in-repo blob schedules top out at 12 blobs per transaction/block.
	transactionSSZMaxBlobHashes = 12
)

// MarshalSSZ ssz marshals the AccessTuple object.
func (a *AccessTuple) MarshalSSZ() ([]byte, error) {
	return ssz.MarshalSSZ(a)
}

// MarshalSSZTo ssz marshals the AccessTuple object to a target array.
func (a *AccessTuple) MarshalSSZTo(buf []byte) (dst []byte, err error) {
	dst = buf
	offset := 24

	if a.Address == nil {
		a.Address = new(H160)
	}
	if dst, err = a.Address.MarshalSSZTo(dst); err != nil {
		return
	}

	dst = ssz.WriteOffset(dst, offset)

	if size := len(a.StorageKeys); size > 65536 {
		err = ssz.ErrListTooBigFn("--.StorageKeys", size, 65536)
		return
	}
	for i := 0; i < len(a.StorageKeys); i++ {
		if a.StorageKeys[i] == nil {
			a.StorageKeys[i] = new(H256)
		}
		if dst, err = a.StorageKeys[i].MarshalSSZTo(dst); err != nil {
			return
		}
	}

	return
}

// UnmarshalSSZ ssz unmarshals the AccessTuple object.
func (a *AccessTuple) UnmarshalSSZ(buf []byte) error {
	var err error
	size := uint64(len(buf))
	if size < 24 {
		return ssz.ErrSize
	}

	tail := buf
	var o1 uint64

	if a.Address == nil {
		a.Address = new(H160)
	}
	if err = a.Address.UnmarshalSSZ(buf[0:20]); err != nil {
		return err
	}

	if o1 = ssz.ReadOffset(buf[20:24]); o1 > size {
		return ssz.ErrOffset
	}
	if o1 < 24 {
		return ssz.ErrInvalidVariableOffset
	}

	buf = tail[o1:]
	num, err := ssz.DivideInt2(len(buf), 32, 65536)
	if err != nil {
		return err
	}
	a.StorageKeys = make([]*H256, num)
	for i := 0; i < num; i++ {
		if a.StorageKeys[i] == nil {
			a.StorageKeys[i] = new(H256)
		}
		if err = a.StorageKeys[i].UnmarshalSSZ(buf[i*32 : (i+1)*32]); err != nil {
			return err
		}
	}
	return nil
}

// SizeSSZ returns the ssz encoded size in bytes for the AccessTuple object.
func (a *AccessTuple) SizeSSZ() (size int) {
	size = 24
	size += len(a.StorageKeys) * 32
	return
}

// HashTreeRoot ssz hashes the AccessTuple object.
func (a *AccessTuple) HashTreeRoot() ([32]byte, error) {
	return ssz.HashWithDefaultHasher(a)
}

// HashTreeRootWith ssz hashes the AccessTuple object with a hasher.
func (a *AccessTuple) HashTreeRootWith(hh *ssz.Hasher) (err error) {
	indx := hh.Index()

	if a.Address == nil {
		a.Address = new(H160)
	}
	if err = a.Address.HashTreeRootWith(hh); err != nil {
		return
	}

	{
		subIndx := hh.Index()
		num := uint64(len(a.StorageKeys))
		if num > 65536 {
			err = ssz.ErrIncorrectListSize
			return
		}
		for _, key := range a.StorageKeys {
			if key == nil {
				key = new(H256)
			}
			if err = key.HashTreeRootWith(hh); err != nil {
				return
			}
		}
		hh.MerkleizeWithMixin(subIndx, num, 65536)
	}

	hh.Merkleize(indx)
	return
}

func sszMarshalH256List(dst []byte, values []*H256, fieldName string, max int) ([]byte, error) {
	if size := len(values); size > max {
		return nil, ssz.ErrListTooBigFn(fieldName, size, max)
	}
	for i := 0; i < len(values); i++ {
		if values[i] == nil {
			values[i] = new(H256)
		}
		var err error
		if dst, err = values[i].MarshalSSZTo(dst); err != nil {
			return nil, err
		}
	}
	return dst, nil
}

func sszUnmarshalH256List(buf []byte, max int) ([]*H256, error) {
	num, err := ssz.DivideInt2(len(buf), 32, max)
	if err != nil {
		return nil, err
	}
	values := make([]*H256, num)
	for i := 0; i < num; i++ {
		values[i] = new(H256)
		if err := values[i].UnmarshalSSZ(buf[i*32 : (i+1)*32]); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func sszHashH256List(hh *ssz.Hasher, values []*H256, max uint64) error {
	subIndx := hh.Index()
	num := uint64(len(values))
	if num > max {
		return ssz.ErrIncorrectListSize
	}
	for _, value := range values {
		if value == nil {
			value = new(H256)
		}
		if err := value.HashTreeRootWith(hh); err != nil {
			return err
		}
	}
	hh.MerkleizeWithMixin(subIndx, num, max)
	return nil
}

func sszMarshalAccessTupleList(dst []byte, tuples []*AccessTuple, fieldName string, max int) ([]byte, error) {
	if size := len(tuples); size > max {
		return nil, ssz.ErrListTooBigFn(fieldName, size, max)
	}
	offset := 4 * len(tuples)
	for i := 0; i < len(tuples); i++ {
		dst = ssz.WriteOffset(dst, offset)
		if tuples[i] == nil {
			tuples[i] = new(AccessTuple)
		}
		offset += tuples[i].SizeSSZ()
	}
	for i := 0; i < len(tuples); i++ {
		var err error
		if dst, err = tuples[i].MarshalSSZTo(dst); err != nil {
			return nil, err
		}
	}
	return dst, nil
}

func sszUnmarshalAccessTupleList(buf []byte, max int) ([]*AccessTuple, error) {
	num, err := ssz.DecodeDynamicLength(buf, max)
	if err != nil {
		return nil, err
	}
	tuples := make([]*AccessTuple, num)
	err = ssz.UnmarshalDynamic(buf, num, func(indx int, b []byte) error {
		tuples[indx] = new(AccessTuple)
		return tuples[indx].UnmarshalSSZ(b)
	})
	if err != nil {
		return nil, err
	}
	return tuples, nil
}

func sszSizeAccessTupleList(tuples []*AccessTuple) int {
	size := 0
	for i := 0; i < len(tuples); i++ {
		size += 4
		if tuples[i] != nil {
			size += tuples[i].SizeSSZ()
		}
	}
	return size
}

func sszHashAccessTupleList(hh *ssz.Hasher, tuples []*AccessTuple, max uint64) error {
	subIndx := hh.Index()
	num := uint64(len(tuples))
	if num > max {
		return ssz.ErrIncorrectListSize
	}
	for _, tuple := range tuples {
		if tuple == nil {
			tuple = new(AccessTuple)
		}
		if err := tuple.HashTreeRootWith(hh); err != nil {
			return err
		}
	}
	hh.MerkleizeWithMixin(subIndx, num, max)
	return nil
}
