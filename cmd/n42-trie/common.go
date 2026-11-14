package main

import (
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
)

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

func tableCfg(table string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		d[table] = kv.TableCfgItem{}
		return d
	}
}
