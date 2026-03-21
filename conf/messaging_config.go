// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// MessagingCfg controls the decentralized messaging relay service.
type MessagingCfg struct {
	Enabled                bool   `json:"enabled" yaml:"enabled"`
	MaxMessageSize         int    `json:"max_message_size" yaml:"max_message_size"`
	StoreCapacity          int    `json:"store_capacity" yaml:"store_capacity"`
	StoreTTLSec            int    `json:"store_ttl" yaml:"store_ttl"`
	RLNEnabled             bool   `json:"rln_enabled" yaml:"rln_enabled"`
	RLNRateLimit           int    `json:"rln_rate_limit" yaml:"rln_rate_limit"`
	RLNEpochSec            int    `json:"rln_epoch_sec" yaml:"rln_epoch_sec"`
	RLNMessageLimit        int    `json:"rln_message_limit" yaml:"rln_message_limit"`
	RLNMerkleDepth         int    `json:"rln_merkle_depth" yaml:"rln_merkle_depth"`
	TopicPrefix            string `json:"topic_prefix" yaml:"topic_prefix"`
	EncryptionEnabled      bool   `json:"encryption_enabled" yaml:"encryption_enabled"`
	KeyRotationIntervalSec int    `json:"key_rotation_interval" yaml:"key_rotation_interval"`
	P2PRelayEnabled        bool   `json:"p2p_relay_enabled" yaml:"p2p_relay_enabled"`
	MessageShards          int    `json:"message_shards" yaml:"message_shards"`
	StoreQueryEnabled      bool   `json:"store_query_enabled" yaml:"store_query_enabled"`
	MaxEnvelopeSize        int    `json:"max_envelope_size" yaml:"max_envelope_size"`
	DeduplicateCacheSize   int    `json:"deduplicate_cache_size" yaml:"deduplicate_cache_size"`
	PersistenceEnabled     bool   `json:"persistence_enabled" yaml:"persistence_enabled"`
	PersistenceMaxAgeSec   int    `json:"persistence_max_age" yaml:"persistence_max_age"`
	PersistenceMaxSizeMB   int    `json:"persistence_max_size_mb" yaml:"persistence_max_size_mb"`
	GroupsEnabled          bool   `json:"groups_enabled" yaml:"groups_enabled"`
	MaxGroupSize           int    `json:"max_group_size" yaml:"max_group_size"`
	MaxGroupsPerNode       int    `json:"max_groups_per_node" yaml:"max_groups_per_node"`
	StreamServerEnabled    bool   `json:"stream_server_enabled" yaml:"stream_server_enabled"`
	StreamServerPort       int    `json:"stream_server_port" yaml:"stream_server_port"`
	DIDEnabled             bool   `json:"did_enabled" yaml:"did_enabled"`
}

func DefaultMessagingCfg() MessagingCfg {
	return MessagingCfg{
		Enabled:                false,
		MaxMessageSize:         65536,
		StoreCapacity:          10000,
		StoreTTLSec:            3600,
		RLNRateLimit:           10,
		RLNEpochSec:            10,
		RLNMessageLimit:        1,
		RLNMerkleDepth:         20,
		TopicPrefix:            "/n42/msg/",
		EncryptionEnabled:      false,
		KeyRotationIntervalSec: 86400, // 24 hours
		P2PRelayEnabled:        false,
		MessageShards:          8,
		MaxEnvelopeSize:        262144,
		DeduplicateCacheSize:   65536,
		PersistenceEnabled:     false,
		PersistenceMaxAgeSec:   86400, // 24 hours
		PersistenceMaxSizeMB:   256,
		GroupsEnabled:          false,
		MaxGroupSize:           256,
		MaxGroupsPerNode:       100,
		StreamServerEnabled:    false,
		StreamServerPort:       8554,
		DIDEnabled:             false,
	}
}
