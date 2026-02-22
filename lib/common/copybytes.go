package common

// CopyBytes returns an exact copy of the provided bytes.
func CopyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	copiedBytes := make([]byte, len(b))
	copy(copiedBytes, b)
	return copiedBytes
}
