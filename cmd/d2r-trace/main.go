package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/codetrek/d2r-server/internal/lifecycletrace"
	"github.com/codetrek/d2r-server/internal/transporttrace"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("d2r-trace", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "trace from a finished owned process or verified closed capture window")
	format := flags.String("format", "transport", "capture format: transport or lifecycle")
	output := flags.String("report", "", "new JSON report path; exclusive creation with mode 0600")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: d2r-trace -input TRACEPATH [-format transport|lifecycle] [-report NEW_JSON]")
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
	var decode captureDecoder
	switch *format {
	case "transport":
		decode = readTransport
	case "lifecycle":
		decode = readLifecycle
	default:
		fmt.Fprintln(stderr, "d2r-trace: format must be transport or lifecycle")
		return 2
	}
	if err := process(*input, *output, stdout, decode); err != nil {
		fmt.Fprintf(stderr, "d2r-trace: %v\n", err)
		return 1
	}
	return 0
}

type captureReport struct {
	value    any
	eligible bool
	problems []string
}

type captureDecoder func(io.ReaderAt, int64) (captureReport, error)

func readTransport(input io.ReaderAt, size int64) (captureReport, error) {
	report, err := transporttrace.Read(input, size)
	if err != nil {
		return captureReport{}, err
	}
	return captureReport{report, report.CaptureIntegrityEligible, report.Problems}, nil
}

func readLifecycle(input io.ReaderAt, size int64) (captureReport, error) {
	report, err := lifecycletrace.Read(input, size)
	if err != nil {
		return captureReport{}, err
	}
	return captureReport{report, report.CaptureIntegrityEligible, report.Problems}, nil
}

func process(input, output string, stdout io.Writer, decode captureDecoder) (resultErr error) {
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
	report, err := decode(file, info.Size())
	if err != nil {
		return err
	}
	if output == "" {
		if err := json.NewEncoder(stdout).Encode(report.value); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	} else {
		if err := writeReport(output, report.value); err != nil {
			return err
		}
	}
	if !report.eligible {
		return fmt.Errorf("capture integrity is not eligible: %v", report.problems)
	}
	return nil
}

func writeReport(path string, report any) error {
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
