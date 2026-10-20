package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codetrek/d2r-server/internal/transporttrace"
)

func traceFile(t *testing.T, closed bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "owned.trace")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 256)
	copy(b, "D2RTP002")
	binary.LittleEndian.PutUint32(b[8:], 2)
	binary.LittleEndian.PutUint32(b[12:], 256)
	binary.LittleEndian.PutUint64(b[16:], transporttrace.Capacity)
	binary.LittleEndian.PutUint64(b[24:], 256)
	flags := uint64(3)
	if closed {
		flags |= 4
	}
	binary.LittleEndian.PutUint64(b[88:], flags)
	binary.LittleEndian.PutUint64(b[96:], 42)
	binary.LittleEndian.PutUint64(b[104:], transporttrace.ObservationCap)
	for i, s := range []string{"30384eabe394cbc6b6a0b7f3788983d58dc14a92b7abe1638853dc4833cf81f1", "d48f2cc33f3e506d623f873ba447c88fe0aea989b98c4057e2481b6ca3988fa2"} {
		v, _ := hex.DecodeString(s)
		copy(b[112+32*i:], v)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(transporttrace.Capacity); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStdoutAndPrivateReport(t *testing.T) {
	input := traceFile(t, true)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-input", input}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, &stderr)
	}
	var r transporttrace.Report
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.CaptureIntegrityEligible || r.G0Qualified || r.GameWorkerQualified || len(r.Kinds) != 6 {
		t.Fatalf("wrong qualification %+v", r)
	}
	if strings.Contains(stdout.String(), input) || strings.Contains(stdout.String(), "owner_thread") {
		t.Fatal("report exposes private input or thread identity")
	}
	output := filepath.Join(t.TempDir(), "new.json")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-input", input, "-report", output}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, &stderr)
	}
	if stdout.Len() != 0 {
		t.Fatal("file report unexpectedly duplicated to stdout")
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("report permissions %o", info.Mode().Perm())
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-input", input, "-report", output}, &stdout, &stderr); code != 1 {
		t.Fatalf("existing output code %d", code)
	}
	after, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing report overwritten")
	}
}

func TestArgumentAndInputFailures(t *testing.T) {
	for _, args := range [][]string{nil, {"-unknown"}, {"-input", "x", "unexpected"}, {"-input"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("arguments %v code %d", args, code)
		}
	}
	var out, err bytes.Buffer
	if code := run([]string{"-help"}, &out, &err); code != 0 || !strings.Contains(err.String(), "private Darwin") {
		t.Fatalf("help: %d %s", code, &err)
	}
	for _, input := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if code := run([]string{"-input", input}, &out, &err); code != 1 {
			t.Fatalf("input %s code %d", input, code)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.trace")
	if e := os.WriteFile(bad, []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if code := run([]string{"-input", bad}, &out, &err); code != 1 {
		t.Fatalf("invalid input code %d", code)
	}
}

func TestIncompleteCaptureStillReportsAndFails(t *testing.T) {
	input := traceFile(t, false)
	var out, err bytes.Buffer
	if code := run([]string{"-input", input}, &out, &err); code != 1 {
		t.Fatalf("incomplete capture code %d", code)
	}
	var r transporttrace.Report
	if e := json.Unmarshal(out.Bytes(), &r); e != nil {
		t.Fatal(e)
	}
	if r.CaptureIntegrityEligible || !r.CommittedPrefixValid || !strings.Contains(err.String(), "provisional_prefix") {
		t.Fatal("incomplete capture not explained")
	}
	output := filepath.Join(t.TempDir(), "incomplete.json")
	if code := run([]string{"-input", input, "-report", output}, &out, &err); code != 1 {
		t.Fatalf("incomplete file report code %d", code)
	}
	if _, e := os.Stat(output); e != nil {
		t.Fatal("incomplete report was not preserved", e)
	}
}

func TestOutputFailures(t *testing.T) {
	input := traceFile(t, true)
	var stderr bytes.Buffer
	if err := process(input, "", brokenWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("stdout error chain %v", err)
	}
	if code := run([]string{"-input", input, "-report", filepath.Join(t.TempDir(), "missing", "out.json")}, io.Discard, &stderr); code != 1 {
		t.Fatalf("bad output directory code %d", code)
	}
	if code := run([]string{"-input", input, "-report", input}, io.Discard, &stderr); code != 1 {
		t.Fatalf("input replacement code %d", code)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
