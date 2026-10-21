package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/codetrek/d2r-server/internal/piperelay"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("d2r-relay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	report := flags.String("report", "", "new private JSON path, exclusive creation with mode 0600")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: d2r-relay -report NEW_JSON\nReads framed stdin and echoes framed stdout for one owned-child pipe session.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *report == "" || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	_, err := piperelay.Serve(stdin, stdout, func(value piperelay.Report) error { return writeReport(*report, value) })
	if err != nil {
		fmt.Fprintf(stderr, "d2r-relay: %v\n", err)
		return 1
	}
	return 0
}

func writeReport(path string, report piperelay.Report) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create relay report: %w", err)
	}
	writeErr := json.NewEncoder(file).Encode(report)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		if cleanupErr := os.Remove(path); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove incomplete relay report: %w", cleanupErr))
		}
		return fmt.Errorf("write relay report: %w", err)
	}
	return nil
}
