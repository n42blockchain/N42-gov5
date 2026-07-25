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
// Prometheus metric counters for JSON-RPC dispatch.
// rpcRequestGauge and failedRequestGauge track total and failed
// request counts. createRPCMetricsLabel formats a per-method label
// string with success/failure status for use as a series key.
// rpcMetricsLabels caches the (valid, failure) label maps.

package jsonrpc

import (
	"fmt"
	"sync"

	prometheus "github.com/n42blockchain/N42/common/metrics"
)

var (
	// rpcMetricsLabelsMu guards rpcMetricsLabels. Every served JSON-RPC call
	// reaches newRPCServingTimerMS from its own handler goroutine, so the
	// unsynchronized map this used to be crashed the process outright once two
	// calls for an unseen method raced:
	//
	//	fatal error: concurrent map writes
	//	  jsonrpc.newRPCServingTimerMS metrics.go:52
	//
	// Observed on a live validator under concurrent eth_sendRawTransaction load
	// — a public RPC endpoint is enough to take a node down.
	rpcMetricsLabelsMu sync.RWMutex
	rpcMetricsLabels   = map[bool]map[string]string{
		true:  {},
		false: {},
	}
	rpcRequestGauge    = prometheus.GetOrCreateCounter("rpc_total")
	failedRequestGauge = prometheus.GetOrCreateCounter("rpc_failure")
)

func createRPCMetricsLabel(method string, valid bool) string {
	status := "failure"
	if valid {
		status = "success"
	}
	return fmt.Sprintf(`rpc_duration_seconds{method="%s",success="%s"}`, method, status)
}

func newRPCServingTimerMS(method string, valid bool) prometheus.Summary {
	rpcMetricsLabelsMu.RLock()
	label, ok := rpcMetricsLabels[valid][method]
	rpcMetricsLabelsMu.RUnlock()
	if !ok {
		label = createRPCMetricsLabel(method, valid)
		rpcMetricsLabelsMu.Lock()
		rpcMetricsLabels[valid][method] = label
		rpcMetricsLabelsMu.Unlock()
	}
	return prometheus.GetOrCreateSummary(label)
}
