// gov5-replay prepares unique signed transactions and qualifies their raw wire
// transport. Its separate explicit strict mode submits original signed bytes
// to an existing performance execution owner; only canonical counters measure TPS.
// Preparation and qualification require separate BOX hardware claim windows.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func runSupply(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gov5-replay prepare|qualify|strict|recover|resume-plan|bounded-plan|bounded-window|resume-four-frame [options]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", "", "absolute fresh private corpus directory")
	switch args[0] {
	case "bounded-window":
		runtime := flags.String("runtime-manifest", "", "Retained schema-2 runtime; independently armed GSR2 owner required")
		attempt := flags.String("attempt", "", "New bounded measurement evidence basename")
		plan := flags.String("plan", "", "Pinned original-source prefix plan v2")
		planSHA := flags.String("plan-sha256", "", "Exact bounded plan file SHA256")
		planID := flags.String("expected-plan-id", "", "Locally pinned expected bounded plan ID")
		baseline := flags.String("baseline-result", "", "Pinned historical full account vector; fresh owner verification remains required")
		baselineSHA := flags.String("baseline-sha256", "", "Exact account-vector report SHA256, also bound in plan")
		rate := flags.Uint64("offered-tps", 6_500_000, "Offered token rate; canonical TPS measured separately over complete 20 seconds")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		digest, err := resumeHashText(*planSHA)
		if err != nil {
			return err
		}
		id, err := resumeHashText(*planID)
		if err != nil {
			return err
		}
		vectorHash, err := resumeHashText(*baselineSHA)
		if err != nil || flags.NArg() != 0 || *runtime == "" || *attempt == "" || *plan == "" || *baseline == "" {
			return errors.New("bounded-window requires explicit plan/hash/ID/runtime/vector/new attempt")
		}
		return runBoundedWindow(ctx, *root, *runtime, *attempt, resumePinnedInput{Path: *plan, SHA256: digest}, resumePinnedInput{Path: *baseline, SHA256: vectorHash}, id, *rate)
	case "resume-four-frame":
		runtime := flags.String("runtime-manifest", "", "Retained schema-2 runtime; owner must already be independently armed")
		attempt := flags.String("attempt", "", "New evidence basename; never replay an existing attempt")
		plan := flags.String("plan", "", "Fixed original four-frame plan file")
		planSHA := flags.String("plan-sha256", "", "Exact plan file SHA256")
		planID := flags.String("expected-plan-id", "", "Locally selected expected plan ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		digest, err := resumeHashText(*planSHA)
		if err != nil {
			return err
		}
		id, err := resumeHashText(*planID)
		if err != nil || flags.NArg() != 0 || *plan == "" || *runtime == "" || *attempt == "" {
			return errors.New("four-frame resume requires explicit plan/hash/ID/runtime/new attempt")
		}
		return runResumeFourFrame(ctx, *root, *runtime, *attempt, resumePinnedInput{Path: *plan, SHA256: digest}, id)
	case "resume-plan", "bounded-plan":
		var nonceEnd *uint64
		if args[0] == "bounded-plan" {
			nonceEnd = flags.Uint64("nonce-end-exclusive", 10000, "Hard original nonce prefix; production profile requires 10000")
		}
		runtime := flags.String("runtime-manifest", "", "fixed schema-2 runtime metadata; no runtime is started")
		outer := flags.String("recovery-outer", "", "fixed promoted historical recovery report")
		recovery := flags.String("recovery-result", "", "fixed full historical canonical account-vector report")
		original := flags.String("original-result", "", "fixed promoted original restoration report")
		output := flags.String("output", "", "fresh validation artifact's plan-output directory")
		manifestHash := flags.String("manifest-sha256", "", "exact finalized source manifest SHA256")
		runtimeHash := flags.String("runtime-sha256", "", "exact runtime metadata SHA256")
		outerHash := flags.String("recovery-outer-sha256", "", "exact promoted recovery report SHA256")
		recoveryHash := flags.String("recovery-sha256", "", "exact full account-vector report SHA256")
		originalHash := flags.String("original-sha256", "", "exact original restoration report SHA256")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || !stringsHasPrefixPath(*root, supplyRoot) {
			return errors.New("resume-plan requires an explicit isolated source and no positional arguments")
		}
		inputs := resumePlanInputs{SourceRoot: *root}
		paths := []string{filepath.Join(*root, "manifest.json"), *runtime, *outer, *recovery, *original}
		hashes := []string{*manifestHash, *runtimeHash, *outerHash, *recoveryHash, *originalHash}
		pins := []*resumePinnedInput{&inputs.Manifest, &inputs.Runtime, &inputs.Outer, &inputs.Recovery, &inputs.Original}
		for i, pin := range pins {
			hash, err := resumeHashText(hashes[i])
			if err != nil || paths[i] == "" {
				return errors.New("resume-plan requires all explicit report paths and SHA256 pins")
			}
			*pin = resumePinnedInput{Path: paths[i], SHA256: hash}
		}
		if nonceEnd != nil {
			if *nonceEnd != 10000 {
				return errors.New("production bounded prefix requires nonce end 10000")
			}
			return runBoundedResumePlanOnly(ctx, inputs, *output, strictRuntimeBase, "/data/blockchain/gov5-work/validation", *nonceEnd)
		}
		return runResumePlanOnly(ctx, inputs, *output, strictRuntimeBase, "/data/blockchain/gov5-work/validation")
	case "recover":
		runtime := flags.String("runtime-manifest", "", "retained schema-2 runtime; no transaction dispatch")
		attempt := flags.String("attempt", "", "new recovery evidence basename")
		baseline := flags.String("baseline-result", "", "failed strict result in this same runtime")
		roots := flags.Bool("require-canonical-roots", false, "Require matching header/QMDB roots in every canonical account batch and a final reread")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *runtime == "" || *baseline == "" {
			return errors.New("recover requires explicit runtime/baseline and no positional arguments")
		}
		return runStrictRecoveryWithRoots(ctx, *root, *runtime, *attempt, *baseline, *roots)
	case "prepare":
		template := flags.String("genesis-template", "", "explicit private n42 performance genesis template")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *template == "" {
			return errors.New("explicit template and no positional arguments required")
		}
		// The initial shape has 192M distinct sender/nonce pairs. No corpus
		// looping, nonce patching, or default chain configuration is supported.
		return prepareCorpus(ctx, *root, *template, supplyRoot, 6000, 32000, 64)
	case "strict":
		runtime := flags.String("runtime-manifest", "", "explicit fresh schema-2 performance runtime manifest")
		attempt := flags.String("attempt", "", "new private strict result basename")
		rate := flags.Uint64("offered-tps", 6_500_000, "global offered target; backpressure and actual canonical rate are separate")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *runtime == "" {
			return errors.New("strict mode requires explicit runtime and no positional arguments")
		}
		return runStrict(ctx, *root, *runtime, *attempt, *rate)
	case "qualify":
		attempt := flags.String("attempt", "", "new result directory basename; existing attempts are retained")
		rate := flags.Uint64("offered-tps", 6_500_000, "global paced raw wire offer rate (not chain admission)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		return qualifySupply(ctx, *root, *attempt, *rate, 4*time.Second, 20*time.Second)
	default:
		return errors.New("supported commands are prepare, qualify, strict, recover, resume-plan, bounded-plan, bounded-window and resume-four-frame")
	}
}

func main() {
	syscall.Umask(0077)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runSupply(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
