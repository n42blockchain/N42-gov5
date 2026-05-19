package main

import (
	"fmt"
	"os"
	"github.com/n42blockchain/N42/internal/bundle"
)

func main() {
	if len(os.Args) != 2 { os.Exit(1) }
	m, err := bundle.Build(os.Args[1], bundle.BuildOptions{
		ChainID: 1,
		Algorithm: bundle.AlgoBlake2b256,
	})
	if err != nil { fmt.Println(err); os.Exit(1) }
	if err := m.Save(os.Args[1] + "/manifest.json"); err != nil { fmt.Println(err); os.Exit(1) }
	fmt.Printf("seeded %d files with %s\n", len(m.Files), m.Algorithm)
}
