// ddn-benchmark collects finite log snapshots and evaluates native System1 rules.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/n42blockchain/N42/internal/ddn/benchmark"
	"github.com/n42blockchain/N42/internal/ddn/gateway"
	"github.com/n42blockchain/N42/internal/ddn/native"
)

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if stat, err := f.Stat(); err != nil {
		return nil, err
	} else if !stat.Mode().IsRegular() || stat.Size() > 64<<20 {
		return nil, errors.New("input must be a regular snapshot of at most 64 MiB")
	}
	s := bufio.NewScanner(io.LimitReader(f, 64<<20))
	s.Buffer(make([]byte, 4096), 1<<20)
	rows := []T{}
	for s.Scan() {
		if strings.TrimSpace(s.Text()) == "" {
			continue
		}
		var row T
		if err := json.Unmarshal(s.Bytes(), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
		if len(rows) > 100000 {
			return nil, errors.New("too many events")
		}
	}
	return rows, s.Err()
}
func run() error {
	mode := flag.String("mode", "rules", "collect, rules or score")
	input := flag.String("input", "", "log snapshot or adjudicated corpus JSONL")
	output := flag.String("output", "", "new output file")
	predictions := flag.String("predictions", "", "prediction JSONL for score")
	source := flag.String("source", "node", "event source for collect")
	prefix := flag.String("prefix", "event", "event ID prefix for collect")
	flag.Parse()
	if *input == "" || *output == "" {
		return errors.New("input and output required")
	}
	var rows []benchmark.Event
	var preds []benchmark.Prediction
	var report benchmark.Report
	switch *mode {
	case "collect":
		if !benchmark.ValidSource(*source) {
			return errors.New("invalid source")
		}
		f, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer f.Close()
		if stat, err := f.Stat(); err != nil {
			return err
		} else if !stat.Mode().IsRegular() || stat.Size() > 64<<20 {
			return errors.New("input must be a regular snapshot of at most 64 MiB")
		}
		s := bufio.NewScanner(io.LimitReader(f, 64<<20))
		s.Buffer(make([]byte, 8192), 1<<20)
		stamp := time.Now().UnixMilli()
		line := 0
		for s.Scan() {
			line++
			text := gateway.Redact(strings.ToValidUTF8(s.Text(), "�"))
			if strings.TrimSpace(text) == "" {
				continue
			}
			truncated := len(text) > 8192
			if truncated {
				text = text[:8192]
				for !utf8.ValidString(text) {
					text = text[:len(text)-1]
				}
			}
			e := benchmark.Event{ID: fmt.Sprintf("%s-%d", *prefix, line), Source: *source, Text: text, ObservedAtMs: stamp, Truncated: truncated}
			if err := e.Validate(false); err != nil {
				return err
			}
			rows = append(rows, e)
			if len(rows) > 100000 {
				return errors.New("too many events")
			}
		}
		if err := s.Err(); err != nil {
			return err
		}
	case "rules", "score":
		var err error
		rows, err = readJSONL[benchmark.Event](*input)
		if err != nil {
			return err
		}
		if *mode == "rules" {
			for _, e := range rows {
				if err := e.Validate(true); err != nil {
					return err
				}
				start := time.Now()
				r, err := native.System1(context.Background(), gateway.Redact(e.Text))
				if err != nil {
					return err
				}
				esc := "NO"
				if r.NeedEscalation {
					esc = "YES"
				}
				preds = append(preds, benchmark.Prediction{ID: e.ID, Label: r.Label, NeedEscalation: esc, LatencyMs: float64(time.Since(start).Nanoseconds()) / 1e6, Model: "N42-system1-rules-v1"})
			}
		} else {
			preds, err = readJSONL[benchmark.Prediction](*predictions)
			if err != nil {
				return err
			}
		}
		report, err = benchmark.Evaluate(rows, preds)
		if err != nil {
			return err
		}
	default:
		return errors.New("unknown mode")
	}
	f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if *mode == "score" {
		return enc.Encode(report)
	}
	if *mode == "collect" {
		for _, e := range rows {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
	} else {
		for _, p := range preds {
			if err := enc.Encode(p); err != nil {
				return err
			}
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
