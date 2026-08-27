//go:build !vmstephook

package vm

// stepHook is a no-op in production builds; the vmstephook build tag
// enables the per-opcode observer the differential tests use.
func stepHook(in *EVMInterpreter, pc uint64, op OpCode) {}
