// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/params"
	// Force-load the tracer engines to trigger registration
	_ "github.com/n42blockchain/N42/internal/tracers/js"
	_ "github.com/n42blockchain/N42/internal/tracers/native"
)

const banner = `
 ███╗   ██╗██╗  ██╗██████╗ 
 ████╗  ██║██║  ██║╚════██╗
 ██╔██╗ ██║███████║ █████╔╝
 ██║╚██╗██║╚════██║██╔═══╝ 
 ██║ ╚████║     ██║███████╗
 ╚═╝  ╚═══╝     ╚═╝╚══════╝
`

const usageText = `n42 [options] [command]

快速启动：
  n42                             启动主网全节点
  n42 --testnet                   启动测试网节点
  n42 --http                      启用 HTTP RPC (127.0.0.1:8545)
  n42 --http --http.addr 0.0.0.0  对外开放 RPC

数据同步：
  n42 --data.dir /data/n42        指定数据目录

挖矿/验证：
  n42 --mine --etherbase 0x...    启用挖矿

详细帮助：
  n42 --help                      查看所有选项
  n42 account --help              账户管理命令
  n42 init --help                 初始化命令`

func main() {
	fmt.Print(banner)

	// 使用新的参数结构（已整合所有旧参数）
	flags := AllFlags()

	rootCmd = append(rootCmd, walletCommand, accountCommand, exportCommand, initCommand)
	commands := rootCmd

	app := &cli.App{
		Name:                   "n42",
		Usage:                  "N42 区块链节点",
		UsageText:              usageText,
		Version:                params.VersionWithCommit(params.GitCommit, ""),
		Flags:                  flags,
		Commands:               commands,
		UseShortOptionHandling: true,
		Action:                 appRun,
		Suggest:                true,
		EnableBashCompletion:   true,
		Copyright:              "Copyright 2022-2026 The N42 Authors",
	}

	// 设置帮助模板
	cli.AppHelpTemplate = `{{.Name}} - {{.Usage}}

版本: {{.Version}}

{{.UsageText}}

选项:
{{range .VisibleFlagCategories}}
  {{.Name}}:
  {{range .Flags}}  {{.}}
  {{end}}{{end}}

命令:{{range .VisibleCommands}}
  {{.Name}}{{"\t"}}{{.Usage}}{{end}}

{{.Copyright}}
`

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
