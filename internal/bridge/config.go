package bridge

// Config holds the cross-chain bridge configuration.
type Config struct {
	// Enable the bridge service
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Relayer configuration (N42→ETH ZK proof submission)
	Relayer RelayerConfig `json:"relayer" yaml:"relayer"`

	// DA Publisher configuration (periodic state root anchoring)
	DAPublisher DAPublisherConfig `json:"daPublisher" yaml:"daPublisher"`

	// ETH target chain
	EthRPCEndpoint  string `json:"ethRpcEndpoint" yaml:"ethRpcEndpoint"`
	VerifierAddress string `json:"verifierAddress" yaml:"verifierAddress"`
	BridgeAddress   string `json:"bridgeAddress" yaml:"bridgeAddress"`

	// SP1 prover for header chain proofs
	SP1Endpoint    string `json:"sp1Endpoint" yaml:"sp1Endpoint"`
	SP1GuestBinary string `json:"sp1GuestBinary" yaml:"sp1GuestBinary"`

	// ETH Light Client (reverse bridge: ETH→N42)
	EthLightClientEnabled bool   `json:"ethLightClientEnabled" yaml:"ethLightClientEnabled"`
	EthBeaconEndpoint     string `json:"ethBeaconEndpoint" yaml:"ethBeaconEndpoint"`

	// Hyperlane (multi-chain: N42↔150+ chains)
	HyperlaneEnabled     bool   `json:"hyperlaneEnabled" yaml:"hyperlaneEnabled"`
	HyperlaneMailbox     string `json:"hyperlaneMailbox" yaml:"hyperlaneMailbox"`         // N42-side Mailbox contract
	HyperlaneISMAddress  string `json:"hyperlaneIsmAddress" yaml:"hyperlaneIsmAddress"`   // ZKISM contract address
	HyperlaneN42Domain   uint32 `json:"hyperlaneN42Domain" yaml:"hyperlaneN42Domain"`     // N42 Hyperlane domain ID

	// Router configuration
	RouterCustomRoutes map[uint32]RouteType `json:"routerCustomRoutes" yaml:"routerCustomRoutes"`
}

// DefaultConfig returns the default bridge configuration (disabled).
func DefaultConfig() *Config {
	return &Config{
		Enabled:            false,
		Relayer:            *DefaultRelayerConfig(),
		DAPublisher:        *DefaultDAPublisherConfig(),
		HyperlaneN42Domain: DomainN42,
	}
}
