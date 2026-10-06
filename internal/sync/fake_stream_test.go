package sync

import (
	"context"
	"net"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// fakeConn is a minimal network.Conn whose only meaningful behaviour is
// reporting a fixed remote peer ID -- the only method the RPC handlers under
// test actually call on a stream's Conn().
type fakeConn struct {
	remote peer.ID
	local  peer.ID
}

func (c *fakeConn) Close() error                                 { return nil }
func (c *fakeConn) LocalPeer() peer.ID                            { return c.local }
func (c *fakeConn) RemotePeer() peer.ID                           { return c.remote }
func (c *fakeConn) RemotePublicKey() ic.PubKey                    { return nil }
func (c *fakeConn) ConnState() network.ConnectionState            { return network.ConnectionState{} }
func (c *fakeConn) LocalMultiaddr() ma.Multiaddr                  { return nil }
func (c *fakeConn) RemoteMultiaddr() ma.Multiaddr                 { return nil }
func (c *fakeConn) Stat() network.ConnStats                       { return network.ConnStats{} }
func (c *fakeConn) Scope() network.ConnScope                      { return nil }
func (c *fakeConn) CloseWithError(network.ConnErrorCode) error    { return nil }
func (c *fakeConn) ID() string                                    { return "fake-conn" }
func (c *fakeConn) NewStream(context.Context) (network.Stream, error) {
	return nil, nil
}
func (c *fakeConn) GetStreams() []network.Stream { return nil }
func (c *fakeConn) IsClosed() bool               { return false }
func (c *fakeConn) As(interface{}) bool          { return false }

// fakeStream is a net.Pipe-backed network.Stream implementation, sufficient
// for exercising RPC handlers that only Read/Write/Close the stream, read its
// Conn().RemotePeer(), and set deadlines.
type fakeStream struct {
	pipe     net.Conn
	conn     *fakeConn
	proto    protocol.ID
	closedWr bool
	closedRd bool
}

// newFakeStreamPair returns two fakeStreams backed by a net.Pipe, wired so
// each side's Conn().RemotePeer() reports the OTHER side's peer ID.
func newFakeStreamPair(localID, remoteID peer.ID) (client, server *fakeStream) {
	c1, c2 := net.Pipe()
	client = &fakeStream{pipe: c1, conn: &fakeConn{local: localID, remote: remoteID}}
	server = &fakeStream{pipe: c2, conn: &fakeConn{local: remoteID, remote: localID}}
	return client, server
}

func (s *fakeStream) ID() string                   { return "fake-stream" }
func (s *fakeStream) Protocol() protocol.ID        { return s.proto }
func (s *fakeStream) SetProtocol(id protocol.ID) error {
	s.proto = id
	return nil
}
func (s *fakeStream) Stat() network.Stats     { return network.Stats{} }
func (s *fakeStream) Conn() network.Conn      { return s.conn }
func (s *fakeStream) Read(p []byte) (int, error)  { return s.pipe.Read(p) }
func (s *fakeStream) Write(p []byte) (int, error) { return s.pipe.Write(p) }
func (s *fakeStream) Close() error                { return s.pipe.Close() }
func (s *fakeStream) Scope() network.StreamScope { return nil }
func (s *fakeStream) ResetWithError(_ network.StreamErrorCode) error {
	return s.Reset()
}

func (s *fakeStream) CloseWrite() error {
	s.closedWr = true
	// net.Pipe has no half-close; closing fully is close enough for our
	// handlers, which only care that subsequent writes/reads behave sanely.
	return nil
}

func (s *fakeStream) CloseRead() error {
	s.closedRd = true
	return nil
}

func (s *fakeStream) Reset() error {
	return s.pipe.Close()
}

func (s *fakeStream) SetDeadline(t time.Time) error      { return s.pipe.SetDeadline(t) }
func (s *fakeStream) SetReadDeadline(t time.Time) error  { return s.pipe.SetReadDeadline(t) }
func (s *fakeStream) SetWriteDeadline(t time.Time) error { return s.pipe.SetWriteDeadline(t) }

var _ network.Stream = (*fakeStream)(nil)
