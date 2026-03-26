package bridge

// Config holds the cross-chain bridge configuration.
type Config struct {
	// Enable the bridge service
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Relayer configuration
	Relayer RelayerConfig `json:"relayer" yaml:"relayer"`

	// ETH target chain
	EthRPCEndpoint   string `json:"ethRpcEndpoint" yaml:"ethRpcEndpoint"`
	VerifierAddress  string `json:"verifierAddress" yaml:"verifierAddress"`
	BridgeAddress    string `json:"bridgeAddress" yaml:"bridgeAddress"`

	// SP1 prover for header chain proofs
	SP1Endpoint    string `json:"sp1Endpoint" yaml:"sp1Endpoint"`
	SP1GuestBinary string `json:"sp1GuestBinary" yaml:"sp1GuestBinary"`
}

// DefaultConfig returns the default bridge configuration (disabled).
func DefaultConfig() *Config {
	return &Config{
		Enabled: false,
		Relayer: *DefaultRelayerConfig(),
	}
}
