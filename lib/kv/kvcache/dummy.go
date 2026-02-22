/*
Copyright 2021 Erigon contributors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package kvcache

import (
	"context"

	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
)

// DummyCache - doesn't remember anything - can be used when service is not remote
type DummyCache struct{}

var (
	_ Cache     = (*DummyCache)(nil)
	_ CacheView = (*DummyView)(nil)
)

func NewDummy() *DummyCache { return &DummyCache{} }

func (c *DummyCache) View(_ context.Context, tx kv.Tx) (CacheView, error) {
	return &DummyView{tx: tx}, nil
}
func (c *DummyCache) OnNewBlock(_ *remote.StateChangeBatch) {}
func (c *DummyCache) Len() int                              { return 0 }
func (c *DummyCache) ValidateCurrentRoot(_ context.Context, _ kv.Tx) (*CacheValidationResult, error) {
	return &CacheValidationResult{Enabled: false}, nil
}

type DummyView struct {
	tx kv.Tx
}

func (v *DummyView) Get(k []byte) ([]byte, error)     { return v.tx.GetOne(kv.PlainState, k) }
func (v *DummyView) GetCode(k []byte) ([]byte, error) { return v.tx.GetOne(kv.Code, k) }
