package main

import (
	"fmt"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

func writeRuntimeValidatorConfigs(out manifest, peerIDs []peer.ID, basePort int, queue bool, interval uint64) error {
	for _, v := range out.Validators {
		var peers []string
		for j, id := range peerIDs {
			if j != v.Index {
				peers = append(peers, fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", basePort+j, id))
			}
		}
		// JSON is valid YAML. The existing loader uses YAML tags, so untagged
		// MinerConfig fields are deliberately lower-case, not Go's JSON names.
		config := map[string]any{
			"node":  map[string]any{"data_dir": v.DataDir, "chain": "private", "profile": "n42", "performance_genesis": out.Genesis, "performance_role": "validator", "performance_execution_socket": out.OwnerSocket, "miner": true, "http": false, "ws": false, "auth_rpc": false, "ipc_path": ""},
			"chain": nil,
			"miner": map[string]any{"etherbase": v.Address, "blockintervalms": interval},
			"p2p":   map[string]any{"private_key": filepath.Join(v.DataDir, "p2p.key"), "data_dir": v.DataDir, "local_ip": "127.0.0.1", "host_address": "127.0.0.1", "tcp_port": v.TCPPort, "udp_port": v.TCPPort, "max_peers": 16, "static_peers": peers, "no_discovery": true, "quic_enabled": false, "bootstrap_node_addr": []string{}, "discv5_bootstrap_addr": []string{}, "relay_node_addr": "", "enable_upnp": false},
		}
		if queue {
			node := config["node"].(map[string]any)
			node["performance_owner_queue"] = true
			node["performance_queue_max_transactions"] = sharedvalidator.DefaultOwnerQueueTransactions
			node["performance_queue_max_encoded_bytes"] = sharedexec.MaxQueueBuildEncodedBytes
		}
		if err := writePrivate(v.Config, config); err != nil {
			return err
		}
	}
	return nil
}
