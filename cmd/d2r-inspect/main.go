package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/codetrek/d2r-server/internal/guestlayout"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("d2r-inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options guestlayout.Options
	flags.StringVar(&options.GuestPath, "guest", "", "path to a thin ARM64 Guest Mach-O")
	flags.StringVar(&options.FixupsPath, "fixups", "", "path to matching main.fixups.tsv")
	flags.StringVar(&options.OutputDir, "out", "", "new output directory (parent must exist)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: d2r-inspect -guest PATH -fixups PATH -out NEW_DIRECTORY")
		fmt.Fprintln(stderr, "Reconstructs static analysis bytes; does not execute the game.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || options.GuestPath == "" || options.FixupsPath == "" || options.OutputDir == "" {
		flags.Usage()
		return 2
	}
	manifest, err := guestlayout.Inspect(options)
	if err != nil {
		fmt.Fprintf(stderr, "d2r-inspect: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(manifest); err != nil {
		fmt.Fprintf(stderr, "d2r-inspect: write result metadata: %v\n", err)
		return 1
	}
	return 0
}
