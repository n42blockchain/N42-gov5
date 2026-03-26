package bridge

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	headerProofsGenerated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_header_proofs_generated_total",
		Help: "Total number of header chain ZK proofs generated",
	})

	headerProofsSubmitted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_header_proofs_submitted_total",
		Help: "Total number of header chain proofs submitted to ETH",
	})

	stateProofsGenerated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_state_proofs_generated_total",
		Help: "Total number of JMT state inclusion proofs generated",
	})

	proofLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "bridge_proof_submission_seconds",
		Help:    "Time to submit a header chain proof to Ethereum",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300},
	})

	latestVerifiedBlock = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bridge_latest_verified_block",
		Help: "Latest N42 block verified on Ethereum",
	})


	// ETH Light Client metrics
	ethLightClientUpdates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_eth_light_client_updates_total",
		Help: "Total ETH sync committee updates processed",
	})

	ethLatestFinalizedSlot = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bridge_eth_latest_finalized_slot",
		Help: "Latest ETH slot verified by light client",
	})

	// Hyperlane metrics
	hyperlaneDispatchTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_hyperlane_dispatch_total",
		Help: "Total messages dispatched via Hyperlane",
	})

	hyperlaneReceiveTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_hyperlane_receive_total",
		Help: "Total messages received via Hyperlane",
	})

	// Router metrics
	routerTransfersZK = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_router_transfers_zk_total",
		Help: "Total transfers via ZK proof path",
	})

	routerTransfersHyperlane = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_router_transfers_hyperlane_total",
		Help: "Total transfers via Hyperlane path",
	})
)
