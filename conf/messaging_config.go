// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// MessagingCfg controls the decentralized messaging relay service.
type MessagingCfg struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	MaxMessageSize int    `json:"max_message_size" yaml:"max_message_size"`
	StoreCapacity  int    `json:"store_capacity" yaml:"store_capacity"`
	StoreTTLSec    int    `json:"store_ttl" yaml:"store_ttl"`
	RLNEnabled     bool   `json:"rln_enabled" yaml:"rln_enabled"`
	RLNRateLimit   int    `json:"rln_rate_limit" yaml:"rln_rate_limit"`
	TopicPrefix    string `json:"topic_prefix" yaml:"topic_prefix"`
}

func DefaultMessagingCfg() MessagingCfg {
	return MessagingCfg{
		Enabled:        false,
		MaxMessageSize: 65536,
		StoreCapacity:  10000,
		StoreTTLSec:    3600,
		RLNRateLimit:   10,
		TopicPrefix:    "/n42/msg/",
	}
}
