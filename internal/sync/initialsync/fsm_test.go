package initialsync

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"
)

var errFakeHandler = errors.New("fake handler error")

func TestStateMachineManager_AddFindRemove(t *testing.T) {
	smm := newStateMachineManager()
	if len(smm.machines) != 0 {
		t.Fatalf("expected empty manager")
	}

	start1 := uint256.NewInt(100)
	start2 := uint256.NewInt(200)

	m1 := smm.addStateMachine(start1)
	if m1 == nil {
		t.Fatalf("expected non-nil state machine")
	}
	m2 := smm.addStateMachine(start2)
	if m2 == nil {
		t.Fatalf("expected non-nil state machine")
	}

	if len(smm.keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(smm.keys))
	}

	found, ok := smm.findStateMachine(start1)
	if !ok || found != m1 {
		t.Fatalf("expected to find m1")
	}

	_, ok = smm.findStateMachine(uint256.NewInt(999))
	if ok {
		t.Fatalf("expected not to find nonexistent machine")
	}

	high, err := smm.highestStartSlot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if high.Uint64() != 200 {
		t.Fatalf("expected highest start slot 200, got %d", high.Uint64())
	}

	if err := smm.removeStateMachine(start1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := smm.findStateMachine(start1); ok {
		t.Fatalf("expected m1 removed")
	}
	if len(smm.keys) != 1 {
		t.Fatalf("expected 1 key after removal, got %d", len(smm.keys))
	}

	if err := smm.removeStateMachine(start1); err == nil {
		t.Fatalf("expected error removing already-removed machine")
	}

	if err := smm.removeAllStateMachines(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(smm.machines) != 0 {
		t.Fatalf("expected all machines removed")
	}
}

func TestStateMachineManager_HighestStartSlotEmpty(t *testing.T) {
	smm := newStateMachineManager()
	if _, err := smm.highestStartSlot(); err == nil {
		t.Fatalf("expected error for empty manager")
	}
}

func TestStateMachineManager_AllMachinesInState(t *testing.T) {
	smm := newStateMachineManager()
	if smm.allMachinesInState(stateNew) {
		t.Fatalf("expected false for empty manager")
	}

	m1 := smm.addStateMachine(uint256.NewInt(1))
	m2 := smm.addStateMachine(uint256.NewInt(2))

	if !smm.allMachinesInState(stateNew) {
		t.Fatalf("expected all machines in stateNew")
	}

	m1.setState(stateScheduled)
	if smm.allMachinesInState(stateNew) {
		t.Fatalf("expected false since m1 changed state")
	}
	m2.setState(stateScheduled)
	if !smm.allMachinesInState(stateScheduled) {
		t.Fatalf("expected all machines in stateScheduled")
	}
}

func TestStateMachineManager_String(t *testing.T) {
	smm := newStateMachineManager()
	smm.addStateMachine(uint256.NewInt(1))
	if smm.String() == "" {
		t.Fatalf("expected non-empty string")
	}
}

func TestStateMachine_SetState(t *testing.T) {
	smm := newStateMachineManager()
	m := smm.addStateMachine(uint256.NewInt(1))
	before := m.updated

	// Setting same state should not update timestamp.
	m.setState(stateNew)
	if m.updated != before {
		t.Fatalf("expected updated timestamp unchanged for same state")
	}

	m.setState(stateScheduled)
	if m.state != stateScheduled {
		t.Fatalf("expected state transitioned to scheduled")
	}
}

func TestStateMachine_TriggerNoHandlers(t *testing.T) {
	smm := newStateMachineManager()
	m := smm.addStateMachine(uint256.NewInt(1))
	if err := m.trigger(eventTick, nil); err == nil {
		t.Fatalf("expected error for missing handler")
	}
}

func TestStateMachine_TriggerWithHandler(t *testing.T) {
	smm := newStateMachineManager()
	called := false
	smm.addEventHandler(eventTick, stateNew, func(m *stateMachine, data interface{}) (stateID, error) {
		called = true
		return stateScheduled, nil
	})
	// Adding the same handler twice should not overwrite.
	smm.addEventHandler(eventTick, stateNew, func(m *stateMachine, data interface{}) (stateID, error) {
		t.Fatalf("second handler should not be invoked")
		return stateSkipped, nil
	})

	m := smm.addStateMachine(uint256.NewInt(1))
	if err := m.trigger(eventTick, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatalf("expected handler to be called")
	}
	if m.state != stateScheduled {
		t.Fatalf("expected state transitioned to scheduled")
	}
}

func TestStateMachine_TriggerHandlerError(t *testing.T) {
	smm := newStateMachineManager()
	smm.addEventHandler(eventTick, stateNew, func(m *stateMachine, data interface{}) (stateID, error) {
		return stateNew, errFakeHandler
	})
	m := smm.addStateMachine(uint256.NewInt(1))
	if err := m.trigger(eventTick, nil); err == nil {
		t.Fatalf("expected error propagated from handler")
	}
}

func TestStateMachine_IsFirstIsLast(t *testing.T) {
	smm := newStateMachineManager()
	m1 := smm.addStateMachine(uint256.NewInt(10))
	m2 := smm.addStateMachine(uint256.NewInt(20))
	m3 := smm.addStateMachine(uint256.NewInt(30))

	if !m1.isFirst() {
		t.Fatalf("expected m1 to be first")
	}
	if m1.isLast() {
		t.Fatalf("expected m1 not last")
	}
	if !m3.isLast() {
		t.Fatalf("expected m3 to be last")
	}
	if m3.isFirst() {
		t.Fatalf("expected m3 not first")
	}
	if m2.isFirst() || m2.isLast() {
		t.Fatalf("expected m2 neither first nor last")
	}

	// Empty manager case.
	emptySmm := newStateMachineManager()
	lone := &stateMachine{smm: emptySmm, start: uint256.NewInt(1)}
	if lone.isFirst() || lone.isLast() {
		t.Fatalf("expected false for machine with no keys in manager")
	}
}

func TestStateMachine_String(t *testing.T) {
	smm := newStateMachineManager()
	m := smm.addStateMachine(uint256.NewInt(42))
	s := m.String()
	if s == "" {
		t.Fatalf("expected non-empty string")
	}
}

func TestStateID_String(t *testing.T) {
	cases := map[stateID]string{
		stateNew:        "new",
		stateScheduled:  "scheduled",
		stateDataParsed: "dataParsed",
		stateSkipped:    "skipped",
		stateSent:       "sent",
		stateID(255):    "stateUnknown",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Fatalf("stateID(%d).String() = %q, want %q", state, got, want)
		}
	}
}

func TestEventID_String(t *testing.T) {
	cases := map[eventID]string{
		eventTick:         "tick",
		eventDataReceived: "dataReceived",
		eventID(255):      "eventUnknown",
	}
	for event, want := range cases {
		if got := event.String(); got != want {
			t.Fatalf("eventID(%d).String() = %q, want %q", event, got, want)
		}
	}
}
