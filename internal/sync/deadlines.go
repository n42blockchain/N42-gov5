package sync

import (
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/n42blockchain/N42/log"
)

var defaultReadDuration = ttfbTimeout
var defaultWriteDuration = respTimeout

// SetRPCStreamDeadlines sets read and write deadlines for libp2p-based connection streams.
func SetRPCStreamDeadlines(stream network.Stream) {
	SetStreamReadDeadline(stream, defaultReadDuration)
	SetStreamWriteDeadline(stream, defaultWriteDuration)
}

// SetStreamReadDeadline sets a read deadline on the stream.
//
// NOTE: libp2p uses the system clock for determining the deadline so we use
// time.Now() instead of the synchronized roughtime.Now(). If the system
// time is corrupted (i.e. time does not advance), the node will experience
// issues closing streams, leading to unexpected failures and possible memory leaks.
func SetStreamReadDeadline(stream network.Stream, duration time.Duration) {
	if err := stream.SetReadDeadline(time.Now().Add(duration)); err != nil &&
		!strings.Contains(err.Error(), "stream closed") {
		log.Debug("Could not set stream deadline",
			"peer", stream.Conn().RemotePeer(),
			"protocol", stream.Protocol(),
			"direction", stream.Stat().Direction,
			"err", err,
		)
	}
}

// SetStreamWriteDeadline sets a write deadline on the stream.
//
// NOTE: libp2p uses the system clock for determining the deadline so we use
// time.Now() instead of the synchronized roughtime.Now(). If the system
// time is corrupted (i.e. time does not advance), the node will experience
// issues closing streams, leading to unexpected failures and possible memory leaks.
func SetStreamWriteDeadline(stream network.Stream, duration time.Duration) {
	if err := stream.SetWriteDeadline(time.Now().Add(duration)); err != nil {
		log.Debug("Could not set stream deadline",
			"peer", stream.Conn().RemotePeer(),
			"protocol", stream.Protocol(),
			"direction", stream.Stat().Direction,
			"err", err,
		)
	}
}
