// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// NotifyCfg controls the push notification service for dApp events.
type NotifyCfg struct {
	Enabled             bool `json:"enabled" yaml:"enabled"`
	MaxHistory          int  `json:"max_history" yaml:"max_history"`
	MaxSubscribers      int  `json:"max_subscribers" yaml:"max_subscribers"`
	BufferSize          int  `json:"buffer_size" yaml:"buffer_size"`
	MaxHistoryAddresses int  `json:"max_history_addresses" yaml:"max_history_addresses"`
}

func DefaultNotifyCfg() NotifyCfg {
	return NotifyCfg{
		Enabled:             false,
		MaxHistory:          100,
		MaxSubscribers:      1000,
		BufferSize:          256,
		MaxHistoryAddresses: 10000,
	}
}
