package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

type freezerSenders struct {
	tbl *freezer.FreezerTable
}

func openFreezerSenders(dir string) (*freezerSenders, error) {
	f, err := freezer.New(dir, 0)
	if err != nil {
		return nil, err
	}
	tbl := f.Table("senders")
	if tbl == nil || tbl.Items() == 0 {
		return nil, fmt.Errorf("no senders table in %s", dir)
	}
	return &freezerSenders{tbl: tbl}, nil
}

func (fs *freezerSenders) ReadBlock(blockNum uint64) ([]byte, error) {
	if blockNum >= fs.tbl.Items() {
		return nil, nil
	}
	return fs.tbl.Retrieve(blockNum)
}

func (fs *freezerSenders) Items() uint64 { return fs.tbl.Items() }

type sgSenders struct{ r *ethel.SenderSegmentReader }

func openSgSenders(dir string) (*sgSenders, error) {
	r, err := ethel.OpenSenderStore(dir)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("no senders SegmentStore in %s", dir)
	}
	return &sgSenders{r: r}, nil
}

func (s *sgSenders) ReadBlock(blockNum uint64) ([]byte, error) {
	return s.r.ReadBlock(blockNum)
}

func (s *sgSenders) Items() uint64 { return s.r.MaxBlock() }

type senderReader interface {
	ReadBlock(blockNum uint64) ([]byte, error)
	Items() uint64
}

type srcEntry struct {
	dir      string
	kind     string
	src      senderReader
	checked  int
	mismatch int
	missing  int
}

func tryOpenSenders(dir string) (senderReader, string, error) {
	if fs, err := openFreezerSenders(dir); err == nil {
		return fs, "freezer", nil
	}
	if sg, err := openSgSenders(dir); err == nil {
		return sg, "segmentstore", nil
	}
	return nil, "", fmt.Errorf("no senders found in %s", dir)
}

func main() {
	ancientPath := flag.String("ancient", "d:/geth/geth/chaindata/ancient/chain", "geth ancient")
	startBlock := flag.Uint64("start", 1, "start block")
	endBlock := flag.Uint64("end", 1000000, "end block")
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		dirs = []string{
			"d:/n42-eth01/chain",
			"d:/n42-eth1/chain/freezer",
			"d:/n42-eth037/chain/freezer",
		}
	}

	_ = log.New() // ensure log package init

	srcs := make([]*srcEntry, 0, len(dirs))
	for _, d := range dirs {
		s, kind, err := tryOpenSenders(d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open %s: %v\n", d, err)
			os.Exit(1)
		}
		fmt.Printf("Source: %s [%s] items=%d\n", d, kind, s.Items())
		srcs = append(srcs, &srcEntry{dir: d, kind: kind, src: s})
	}

	ancientF, err := freezer.New(*ancientPath, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open ancient: %v\n", err)
		os.Exit(1)
	}
	ancientF.EnsureTable("bodies", "c")
	bodyTbl := ancientF.Table("bodies")
	if bodyTbl == nil {
		fmt.Fprintf(os.Stderr, "ancient missing bodies table\n")
		os.Exit(1)
	}
	fmt.Printf("Ancient bodies: items=%d\n\n", bodyTbl.Items())

	chainCfg := params.MainnetChainConfig
	var recoverFail int

	for blockNum := *startBlock; blockNum <= *endBlock; blockNum++ {
		bodyData, err := bodyTbl.Retrieve(blockNum)
		if err != nil {
			continue
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil || body == nil {
			continue
		}
		if len(body.Transactions) == 0 {
			continue
		}

		signer := transaction.MakeSigner(chainCfg, new(big.Int).SetUint64(blockNum))
		recovered := make([]types.Address, 0, len(body.Transactions))
		ok := true
		for _, tx := range body.Transactions {
			s, err := transaction.Sender(signer, tx)
			if err != nil {
				ok = false
				break
			}
			recovered = append(recovered, s)
		}
		if !ok {
			recoverFail++
			continue
		}

		for _, e := range srcs {
			data, err := e.src.ReadBlock(blockNum)
			if err != nil || len(data) == 0 {
				e.missing++
				continue
			}
			n := len(data) / 20
			if n != len(recovered) {
				e.mismatch++
				if e.mismatch <= 3 {
					fmt.Printf("  %s block %d: count mismatch stored=%d recovered=%d\n",
						e.dir, blockNum, n, len(recovered))
				}
				continue
			}
			match := true
			for i := 0; i < n; i++ {
				var stored types.Address
				copy(stored[:], data[i*20:(i+1)*20])
				if stored != recovered[i] {
					match = false
					break
				}
			}
			if match {
				e.checked++
			} else {
				e.mismatch++
				if e.mismatch <= 3 {
					fmt.Printf("  %s block %d: addr mismatch\n", e.dir, blockNum)
				}
			}
		}

		if blockNum%100000 == 0 {
			fmt.Printf("block %d:", blockNum)
			for _, e := range srcs {
				fmt.Printf(" [%s ok=%d miss=%d mis=%d]",
					shortName(e.dir), e.checked, e.missing, e.mismatch)
			}
			fmt.Println()
		}
	}

	fmt.Println()
	fmt.Println("=== Final ===")
	for _, e := range srcs {
		status := "OK"
		if e.mismatch > 0 {
			status = "FAIL"
		}
		fmt.Printf("%s [%s]: checked=%d mismatch=%d missing=%d  %s\n",
			e.dir, e.kind, e.checked, e.mismatch, e.missing, status)
	}
	fmt.Printf("ecrecover failures: %d (skipped from comparison)\n", recoverFail)
}

func shortName(d string) string {
	// Take the second-to-last segment ("n42-eth01" from "d:/n42-eth01/chain")
	end := len(d)
	last := -1
	for i := end - 1; i >= 0; i-- {
		if d[i] == '/' || d[i] == '\\' {
			if last < 0 {
				last = i
			} else {
				return d[i+1 : last]
			}
		}
	}
	if last >= 0 {
		return d[:last]
	}
	return d
}
