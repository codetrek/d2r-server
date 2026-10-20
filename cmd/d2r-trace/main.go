package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/codetrek/d2r-server/internal/transporttrace"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("d2r-trace", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "trace from a finished owned process or verified closed capture window")
	output := flags.String("report", "", "new JSON report path; exclusive creation with mode 0600")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: d2r-trace -input TRACEPATH [-report NEW_JSON]")
		fmt.Fprintln(stderr, "Reads a fixed-profile private Darwin ARM64 bounded capture window, not a network capture or G0 qualification.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *input == "" || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	if err := process(*input, *output, stdout); err != nil {
		fmt.Fprintf(stderr, "d2r-trace: %v\n", err)
		return 1
	}
	return 0
}

func process(input, output string, stdout io.Writer) (resultErr error) {
	file, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close input: %w", err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat input: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("trace input must be a regular file")
	}
	report, err := transporttrace.Read(file, info.Size())
	if err != nil {
		return err
	}
	if output == "" {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	} else {
		if err := writeReport(output, report); err != nil {
			return err
		}
	}
	if !report.CaptureIntegrityEligible {
		return fmt.Errorf("capture integrity is not eligible: %v", report.Problems)
	}
	return nil
}

func writeReport(path string, report *transporttrace.Report) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	writeErr := json.NewEncoder(file).Encode(report)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
