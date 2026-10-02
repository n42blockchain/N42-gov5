// dgramPipe is a fake UDP socket (no real network port) used to exercise
// UDPv4 packet handling end-to-end without opening a loopback connection.
// Adapted from go-ethereum's p2p/discover/udp_test.go dgramPipe, updated to
// this package's net.UDPAddr-based UDPConn interface (not netip.AddrPort).
package discover

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/p2p/discover/v4wire"
	"github.com/n42blockchain/N42/internal/p2p/enode"
)

type dgram struct {
	to   net.UDPAddr
	data []byte
}

// dgramPipe queues all sent datagrams for retrieval by the test, and
// delivers packets pushed via inject() to a reader.
type dgramPipe struct {
	mu      *sync.Mutex
	cond    *sync.Cond
	closing chan struct{}
	closed  bool
	queue   []dgram
}

func newpipe() *dgramPipe {
	mu := new(sync.Mutex)
	return &dgramPipe{
		closing: make(chan struct{}),
		cond:    &sync.Cond{L: mu},
		mu:      mu,
	}
}

func (c *dgramPipe) WriteToUDP(b []byte, to *net.UDPAddr) (n int, err error) {
	msg := make([]byte, len(b))
	copy(msg, b)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, errors.New("closed")
	}
	c.queue = append(c.queue, dgram{*to, b})
	c.cond.Signal()
	return len(b), nil
}

func (c *dgramPipe) ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error) {
	<-c.closing
	return 0, nil, io.EOF
}

func (c *dgramPipe) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		close(c.closing)
		c.closed = true
	}
	return nil
}

func (c *dgramPipe) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IP{0, 0, 0, 0}, Port: 30303}
}

// receive waits for the next queued outgoing datagram.
func (c *dgramPipe) receive() (dgram, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.queue) == 0 && !c.closed {
		c.cond.Wait()
	}
	if c.closed {
		return dgram{}, errClosed
	}
	p := c.queue[0]
	c.queue = c.queue[1:]
	return p, nil
}

func newkey() *ecdsa.PrivateKey {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic("couldn't generate key: " + err.Error())
	}
	return key
}

type udpTest struct {
	pipe                *dgramPipe
	table               *Table
	db                  *enode.DB
	udp                 *UDPv4
	localkey, remotekey *ecdsa.PrivateKey
	remoteaddr          *net.UDPAddr
}

func newUDPTest(t testingT) *udpTest {
	test := &udpTest{
		pipe:       newpipe(),
		localkey:   newkey(),
		remotekey:  newkey(),
		remoteaddr: &net.UDPAddr{IP: net.IP{10, 0, 1, 99}, Port: 30303},
	}
	var err error
	test.db, err = enode.OpenDB(context.Background(), "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	ln := enode.NewLocalNode(test.db, test.localkey)
	test.udp, err = ListenV4(test.pipe, ln, Config{PrivateKey: test.localkey})
	if err != nil {
		t.Fatalf("ListenV4: %v", err)
	}
	test.table = test.udp.tab

	select {
	case <-test.table.initDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for table init")
	}
	return test
}

// testingT is the subset of *testing.T this helper needs, so it can be
// shared between tests without importing "testing" into this file's doc
// comment namespace (kept minimal intentionally).
type testingT interface {
	Fatalf(format string, args ...interface{})
}

func (test *udpTest) close() {
	test.udp.Close()
	test.db.Close()
}

// packetIn delivers a packet to the transport as if received from remoteaddr.
func (test *udpTest) packetIn(data v4wire.Packet) error {
	enc, _, err := v4wire.Encode(test.remotekey, data)
	if err != nil {
		return err
	}
	return test.udp.handlePacket(test.remoteaddr, enc)
}
