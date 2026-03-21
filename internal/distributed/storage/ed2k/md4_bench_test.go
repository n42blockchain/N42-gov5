package ed2k

import (
	"testing"
)

func BenchmarkMD4_64B(b *testing.B) {
	data := make([]byte, 64)
	b.SetBytes(64)
	for i := 0; i < b.N; i++ {
		h := NewMD4()
		h.Write(data)
		h.Sum(nil)
	}
}

func BenchmarkMD4_1KB(b *testing.B) {
	data := make([]byte, 1024)
	b.SetBytes(1024)
	for i := 0; i < b.N; i++ {
		h := NewMD4()
		h.Write(data)
		h.Sum(nil)
	}
}

func BenchmarkMD4_1MB(b *testing.B) {
	data := make([]byte, 1024*1024)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		h := NewMD4()
		h.Write(data)
		h.Sum(nil)
	}
}

func BenchmarkEd2kHash_1MB(b *testing.B) {
	data := make([]byte, 1024*1024)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		Hash(data)
	}
}

func BenchmarkEd2kHash_10MB(b *testing.B) {
	data := make([]byte, 10*1024*1024)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		Hash(data)
	}
}
