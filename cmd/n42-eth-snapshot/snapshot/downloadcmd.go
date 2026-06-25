// downloadcmd.go — the shared `download` CLI command, registered on both the
// main-chain `n42` binary and the `eth-el` binary. A minimal, resumable
// snapshot fetch (reth-download style):
//
//	n42    download --minimal --resumable --source <url> --datadir <dir>
//	eth-el download --minimal --resumable --source <url> --datadir <dir>
//
// "Resumable" is inherent to the underlying Fetch: it skips files already
// present+verified and resumes partial files via HTTP Range (.part sidecar),
// so re-running after an interruption only transfers what's missing. With
// --auto the command no-ops when the datadir already holds data, so it can be
// wired into first-boot ("download only when no local data").
package snapshot

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"
)

// DownloadCommand returns the `download` cli.Command shared by n42 and eth-el.
func DownloadCommand() *cli.Command {
	return &cli.Command{
		Name:  "download",
		Usage: "Download a snapshot tier from a manifest source (minimal/full/archive, resumable)",
		Description: "Fetches the files for the selected tier from --source into --datadir.\n" +
			"Resumable by construction: already-complete files are skipped and partial\n" +
			"files resume via HTTP Range, so an interrupted download is restarted by just\n" +
			"re-running the same command. With --auto it only runs when the datadir is empty.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "source", Aliases: []string{"s"}, Usage: "publisher manifest source (https:// or file://)", Required: true},
			&cli.StringFlag{Name: "datadir", Aliases: []string{"d"}, Usage: "destination datadir", Required: true},
			&cli.BoolFlag{Name: "minimal", Usage: "tip-state tier (~26GB): snapshot only"},
			&cli.BoolFlag{Name: "full", Usage: "EIP-4444 tier (~137GB): + 1yr hot bodies/txindex"},
			&cli.BoolFlag{Name: "archive", Usage: "archive tier (~809GB): full-history witness"},
			&cli.StringFlag{Name: "tier", Usage: "explicit tier (minimal|full|archive); overrides the boolean flags"},
			&cli.BoolFlag{Name: "resumable", Value: true, Usage: "skip complete files + resume partials via HTTP Range (always on; flag kept for clarity)"},
			&cli.IntFlag{Name: "parallel", Aliases: []string{"p"}, Value: 4, Usage: "parallel transfers"},
			&cli.BoolFlag{Name: "include-senders", Usage: "also fetch the optional senders pack"},
			&cli.BoolFlag{Name: "dry-run", Usage: "list what would be fetched, transfer nothing"},
			&cli.BoolFlag{Name: "auto", Usage: "auto-enable: only download when the datadir has no data (idempotent first-boot)"},
		},
		Action: func(c *cli.Context) error {
			datadir := c.String("datadir")
			source := c.String("source")
			mode := resolveTier(c)

			if c.Bool("auto") {
				det, err := DetectMode(datadir)
				if err != nil {
					return fmt.Errorf("download --auto: detect datadir: %w", err)
				}
				if det.Mode != "" {
					fmt.Printf("download --auto: datadir already has the %q tier — nothing to do.\n", det.Mode)
					return nil
				}
				fmt.Printf("download --auto: datadir is empty — fetching %q tier from %s\n", mode, source)
			}

			report, err := Fetch(source, datadir, mode, c.Bool("include-senders"), c.Bool("dry-run"), c.Int("parallel"))
			if err != nil {
				return fmt.Errorf("download: %w", err)
			}
			report.Print(os.Stdout)
			if !report.OK {
				return fmt.Errorf("download: tier %q incomplete (see report)", mode)
			}
			return nil
		},
	}
}

// resolveTier maps the --tier / --minimal/--full/--archive flags to a mode
// string, defaulting to the smallest (minimal) when none is given.
func resolveTier(c *cli.Context) string {
	if t := c.String("tier"); t != "" {
		return t
	}
	switch {
	case c.Bool("archive"):
		return "archive"
	case c.Bool("full"):
		return "full"
	default:
		return "minimal"
	}
}

// AutoDownloadIfEmpty is the first-boot startup hook: if the datadir holds no
// data and source is non-empty, fetch the given tier (default "minimal").
// Idempotent — returns nil immediately when data is already present, so node
// startup can call it unconditionally. This is the "auto-enable when no data"
// path equivalent to passing `download --auto`.
func AutoDownloadIfEmpty(datadir, source, mode string, parallel int) (*FetchReport, error) {
	if source == "" {
		return nil, nil
	}
	det, err := DetectMode(datadir)
	if err != nil {
		return nil, err
	}
	if det.Mode != "" {
		return nil, nil // already has data
	}
	if mode == "" {
		mode = "minimal"
	}
	if parallel <= 0 {
		parallel = 4
	}
	return Fetch(source, datadir, mode, false, false, parallel)
}
