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
	"syscall"
	"time"
)

func runSupply(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gov5-replay prepare|qualify|strict [options]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", "", "absolute fresh private corpus directory")
	switch args[0] {
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
		return errors.New("supported commands are prepare, qualify and strict")
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
