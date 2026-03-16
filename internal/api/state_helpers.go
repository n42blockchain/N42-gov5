package api

import (
	"errors"

	"github.com/n42blockchain/N42/modules/state"
)

func requireIntraBlockState(v interface{}, msg string) (*state.IntraBlockState, error) {
	statedb, ok := v.(*state.IntraBlockState)
	if !ok || statedb == nil {
		return nil, errors.New(msg)
	}
	return statedb, nil
}

func requirePlainStateReader(statedb *state.IntraBlockState, msg string) (*state.PlainState, error) {
	if statedb == nil {
		return nil, errors.New(msg)
	}
	reader, ok := statedb.GetStateReader().(*state.PlainState)
	if !ok || reader == nil {
		return nil, errors.New(msg)
	}
	return reader, nil
}
