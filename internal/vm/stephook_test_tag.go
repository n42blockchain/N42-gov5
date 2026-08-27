//go:build vmstephook

package vm

// testStepHook, when set by a test, observes every fetched opcode. It lets
// the differential tests assert that the block precheck performs exactly the
// same opcodes as per-opcode charging — the witness reader is a sequential
// stream, so even a doomed frame must do the same reads in the same order.
var testStepHook func(depth int, pc uint64, op OpCode)

func stepHook(in *EVMInterpreter, pc uint64, op OpCode) {
	if testStepHook != nil {
		testStepHook(in.depth, pc, op)
	}
}
