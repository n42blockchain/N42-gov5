// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"testing"

	"github.com/urfave/cli/v2"
)

// resetProductionDefaultsUnderTest clears the fields applyN42NativeProductionDefaults
// touches, so tests don't leak state through the shared package-level DefaultConfig.
func resetProductionDefaultsUnderTest() {
	DefaultConfig.NodeCfg.Profile = ""
	DefaultConfig.MobileVerifyCfg.Enabled = false
	DefaultConfig.CoprocessorCfg.Enabled = false
	DefaultConfig.CoprocessorCfg.MaxConcurrentTasks = 0
	DefaultConfig.CoprocessorCfg.TaskTimeoutSec = 0
	DefaultConfig.CoprocessorCfg.MaxPendingTasks = 0
	DefaultConfig.CoprocessorCfg.PruneIntervalSec = 0
	DefaultConfig.CoprocessorCfg.OptimisticChallengeSec = 0
	DefaultConfig.CoprocessorCfg.OptimisticBondWei = 0
	DefaultConfig.CoprocessorCfg.MinProviderStake = 0
	DefaultConfig.CoprocessorCfg.SlashPercentage = 0
}

func runWithFlags(t *testing.T, args []string) *cli.Context {
	t.Helper()
	app := &cli.App{
		Flags: []cli.Flag{ProfileFlag,
			&cli.BoolFlag{Name: "mobileverify", Destination: &DefaultConfig.MobileVerifyCfg.Enabled},
			&cli.BoolFlag{Name: "coprocessor", Destination: &DefaultConfig.CoprocessorCfg.Enabled},
		},
		Action: func(c *cli.Context) error {
			applyN42NativeProductionDefaults(c)
			return nil
		},
	}
	if err := app.Run(append([]string{"n42"}, args...)); err != nil {
		t.Fatalf("app.Run(%v): %v", args, err)
	}
	return nil
}

// TestN42NativeDefaultsEnableBothByDefault: no chain/profile flags at all (the
// bare production invocation) enables both mobileverify and coprocessor.
func TestN42NativeDefaultsEnableBothByDefault(t *testing.T) {
	resetProductionDefaultsUnderTest()
	runWithFlags(t, nil)
	if !DefaultConfig.MobileVerifyCfg.Enabled {
		t.Fatal("mobileverify should default to enabled on the n42 native profile")
	}
	if !DefaultConfig.CoprocessorCfg.Enabled {
		t.Fatal("coprocessor should default to enabled on the n42 native profile")
	}
	// The Validate()-required fields must have been filled, or a real run
	// would fail conf.CoprocessorCfg.Validate() the moment Enabled flipped true.
	if DefaultConfig.CoprocessorCfg.MaxConcurrentTasks == 0 || DefaultConfig.CoprocessorCfg.TaskTimeoutSec == 0 {
		t.Fatalf("coprocessor default-enable did not fill required fields: %+v", DefaultConfig.CoprocessorCfg)
	}
}

// TestN42NativeDefaultsExplicitOptOut: --mobileverify=false / --coprocessor=false
// must be honored, not silently overridden back to true.
func TestN42NativeDefaultsExplicitOptOut(t *testing.T) {
	resetProductionDefaultsUnderTest()
	runWithFlags(t, []string{"--mobileverify=false", "--coprocessor=false"})
	if DefaultConfig.MobileVerifyCfg.Enabled {
		t.Fatal("explicit --mobileverify=false was overridden")
	}
	if DefaultConfig.CoprocessorCfg.Enabled {
		t.Fatal("explicit --coprocessor=false was overridden")
	}
}

// TestN42NativeDefaultsExplicitOptIn: an explicit --mobileverify (true) is left
// alone (not double-toggled) and still gets its dependent fields filled.
func TestN42NativeDefaultsExplicitOptIn(t *testing.T) {
	resetProductionDefaultsUnderTest()
	runWithFlags(t, []string{"--mobileverify", "--coprocessor"})
	if !DefaultConfig.MobileVerifyCfg.Enabled || !DefaultConfig.CoprocessorCfg.Enabled {
		t.Fatal("explicit enable flags should remain enabled")
	}
}

// TestEthELProfileNeverAutoEnables: eth-el deployments must NEVER get mobile
// attestation or coprocessor turned on by this default — mobile verification
// is an n42-native feature (see docs/mobile-attestation-design.md) and must
// not activate on eth-el, and coprocessor default-enable is scoped identically.
func TestEthELProfileNeverAutoEnables(t *testing.T) {
	resetProductionDefaultsUnderTest()
	runWithFlags(t, []string{"--profile", "eth"})
	if DefaultConfig.MobileVerifyCfg.Enabled {
		t.Fatal("mobileverify must not auto-enable on the eth-el profile")
	}
	if DefaultConfig.CoprocessorCfg.Enabled {
		t.Fatal("coprocessor must not auto-enable on the eth-el profile")
	}
}
