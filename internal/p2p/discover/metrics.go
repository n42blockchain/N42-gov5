// Copyright 2023 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package discover

import (
	"net"

	"github.com/rcrowley/go-metrics"
)

const (
	moduleName = "discover"
	// ingressMeterName is the prefix of the per-packet inbound metrics.
	ingressMeterName = moduleName + "/ingress"

	// egressMeterName is the prefix of the per-packet outbound metrics.
	egressMeterName = moduleName + "/egress"
)

var (
	ingressTrafficMeter = metrics.NewRegisteredMeter(ingressMeterName, nil)
	egressTrafficMeter  = metrics.NewRegisteredMeter(egressMeterName, nil)
)

// meteredUDPConn is a wrapper around a UDPConn that meters both the
// inbound and outbound network traffic.
type meteredUDPConn struct {
	UDPConn
}

func newMeteredConn(conn UDPConn) UDPConn {
	return &meteredUDPConn{UDPConn: conn}
}

// ReadFromUDP delegates a network read to the underlying connection,
// bumping the UDP ingress traffic meter along the way.
func (c *meteredUDPConn) ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error) {
	n, addr, err = c.UDPConn.ReadFromUDP(b)
	ingressTrafficMeter.Mark(int64(n))
	return n, addr, err
}

// WriteToUDP delegates a network write to the underlying connection,
// bumping the UDP egress traffic meter along the way.
func (c *meteredUDPConn) WriteToUDP(b []byte, addr *net.UDPAddr) (n int, err error) {
	n, err = c.UDPConn.WriteToUDP(b, addr)
	egressTrafficMeter.Mark(int64(n))
	return n, err
}
