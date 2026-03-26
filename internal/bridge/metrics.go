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

	depositsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_deposits_total",
		Help: "Total cross-chain deposits",
	})

	withdrawalsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_withdrawals_total",
		Help: "Total cross-chain withdrawals",
	})

	proofLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "bridge_proof_generation_seconds",
		Help:    "Time to generate a header chain ZK proof",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300},
	})

	latestVerifiedBlock = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bridge_latest_verified_block",
		Help: "Latest N42 block verified on Ethereum",
	})
)
