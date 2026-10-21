package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/d2r-server/internal/piperelay"
)

func input(t *testing.T, frames ...piperelay.Frame) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, f := range frames {
		if err := piperelay.WriteFrame(&b, f); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}
func TestCompleteInputWritesPrivateReportAndOnlyFramedStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	var out, diagnostic bytes.Buffer
	data := input(t, piperelay.Frame{Type: piperelay.C2S, Sequence: 1, ClientID: 19, Payload: []byte("private bytes")}, piperelay.Frame{Type: piperelay.Close, Sequence: 2})
	if code := run([]string{"-report", path}, bytes.NewReader(data), &out, &diagnostic); code != 0 || diagnostic.Len() != 0 {
		t.Fatalf("exit %d: %s", code, diagnostic.String())
	}
	echo, err := piperelay.ReadFrame(&out)
	if err != nil || string(echo.Payload) != "private bytes" || echo.ClientID != 19 {
		t.Fatalf("stdout echo: %+v %v", echo, err)
	}
	ack, err := piperelay.ReadFrame(&out)
	if err != nil || ack.Type != piperelay.Ack || ack.Sequence != 2 || out.Len() != 0 {
		t.Fatalf("stdout Ack: %+v %v", ack, err)
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("report mode: %v %v", stat, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r piperelay.Report
	if err = json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if !r.InputComplete || !r.EOFObserved || !r.ExplicitClose || r.G0Qualified || r.GameWorkerQualified || r.DataFrames != 1 {
		t.Fatalf("report claims: %+v", r)
	}
	for _, private := range []string{"private bytes", "ack_received", "payload\"", "client_id"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatalf("report exposes %s", private)
		}
	}
}
func TestUsageDoesNotWriteProtocolBytes(t *testing.T) {
	for _, args := range [][]string{nil, {"-report"}, {"-unknown"}, {"-report", "path", "unexpected"}} {
		var out, err bytes.Buffer
		if code := run(args, bytes.NewReader(nil), &out, &err); code != 2 || out.Len() != 0 || err.Len() == 0 {
			t.Fatalf("usage: %v exit%d stdout%s stderr%s", args, code, out.String(), err.String())
		}
	}
	var out, err bytes.Buffer
	if code := run([]string{"-h"}, bytes.NewReader(nil), &out, &err); code != 0 || out.Len() != 0 || err.Len() == 0 {
		t.Fatal("help used protocol output")
	}
}
func TestIncompleteInputAndReportCollisionNeverAck(t *testing.T) {
	for _, trailing := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "report.json")
		var in []byte
		if trailing {
			in = append(input(t, piperelay.Frame{Type: piperelay.Close, Sequence: 1}), 0)
		}
		var out, diagnostic bytes.Buffer
		if code := run([]string{"-report", path}, bytes.NewReader(in), &out, &diagnostic); code != 1 || out.Len() != 0 {
			t.Fatalf("partial input acknowledged: %d %s", code, out.String())
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial session report exists: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(path, []byte("preserve existing"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"-report", path}, bytes.NewReader(input(t, piperelay.Frame{Type: piperelay.Close, Sequence: 1})), &out, &diagnostic); code != 1 || out.Len() != 0 {
		t.Fatal("report collision acknowledged")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "preserve existing" {
		t.Fatal("existing report modified")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestAckWriteFailureLeavesTruthfulCompleteInputReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	var diagnostic bytes.Buffer
	if code := run([]string{"-report", path}, bytes.NewReader(input(t, piperelay.Frame{Type: piperelay.Close, Sequence: 1})), brokenWriter{}, &diagnostic); code != 1 || diagnostic.Len() == 0 {
		t.Fatal("failed Ack write reported success")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report piperelay.Report
	if err = json.Unmarshal(body, &report); err != nil || !report.InputComplete {
		t.Fatalf("complete input report: %+v %v", report, err)
	}
}
