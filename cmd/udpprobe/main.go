// udpprobe — one-shot discv4 PING/PONG probe to a known geth bootnode.
//
// Existence check for the firewall fix: if PONG returns, UDP 30303 is
// fully bidirectional (outbound PING + inbound PONG match the
// stateful connection on Windows Firewall). If it times out, the
// inbound rule didn't take effect.

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

func main() {
	target := flag.String("target",
		"enode://d860a01f9722d78051619d1e2351aba3f43f943f6f00718d1b9baa4101932a1f5011f16bb2b1bb35db20d6fe28fa0bf09636d26a87d31de9ec6203eeedb1f666@18.138.108.67:30303",
		"enode to probe")
	flag.Parse()

	n, err := enode.Parse(enode.ValidSchemes, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse enode:", err)
		os.Exit(1)
	}
	fmt.Printf("Target: id=%s ip=%s udp=%d tcp=%d\n",
		n.ID().String()[:16], n.IP(), n.UDP(), n.TCP())

	key, _ := crypto.GenerateKey()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		fmt.Fprintln(os.Stderr, "bind udp:", err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Printf("Local UDP port: %d\n", conn.LocalAddr().(*net.UDPAddr).Port)

	db, _ := enode.OpenDB("")
	localNode := enode.NewLocalNode(db, key)
	localNode.Set(enr.IP(net.IPv4zero))
	localNode.Set(enr.UDP(uint16(conn.LocalAddr().(*net.UDPAddr).Port)))

	udp, err := discover.ListenV4(conn, localNode, discover.Config{
		PrivateKey: key,
		Bootnodes:  []*enode.Node{n},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen v4:", err)
		os.Exit(1)
	}
	defer udp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() { _, err := udp.Ping(n); done <- err }()

	select {
	case err := <-done:
		if err != nil {
			fmt.Printf("PING failed after %v: %v\n", time.Since(start), err)
			os.Exit(2)
		}
		fmt.Printf("PONG received in %v — UDP %d bidirectional OK\n", time.Since(start), n.UDP())
	case <-ctx.Done():
		fmt.Printf("PING timed out after %v — UDP %d still blocked\n", time.Since(start), n.UDP())
		os.Exit(3)
	}
}
