package p2p

import (
	"strconv"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	astLog "github.com/n42blockchain/N42/log"
)

var log = astLog.New("prefix", "p2p")

func logIPAddr(id peer.ID, addrs ...ma.Multiaddr) {
	for _, addr := range addrs {
		addrStr := addr.String()
		if strings.Contains(addrStr, "/ip4/") || strings.Contains(addrStr, "/ip6/") {
			log.Info("P2P server started", "addr", addrStr, "peer_id", id.String()[:16]+"...")
			return
		}
	}
}

func logExternalIPAddr(id peer.ID, addr string, port int) {
	if addr != "" {
		multiAddr, err := MultiAddressBuilder(addr, uint(port))
		if err != nil {
			log.Error("Could not create multiaddress", "err", err)
			return
		}
		log.Info("Node started external p2p server", "multiAddr", multiAddr.String()+"/p2p/"+id.String())
	}
}

func logExternalDNSAddr(id peer.ID, addr string, port int) {
	if addr != "" {
		p := strconv.FormatUint(uint64(port), 10)
		log.Info("Node started external p2p server", "multiAddr", "/dns4/"+addr+"/tcp/"+p+"/p2p/"+id.String())
	}
}
