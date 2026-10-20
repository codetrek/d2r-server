package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestFlagsAndCancelledRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"help", []string{"-h"}, 0, "Usage:"},
		{"unknown flag", []string{"-unknown"}, 2, "flag provided but not defined"},
		{"extra argument", []string{"unexpected"}, 2, "Usage:"},
		{"zero timeout", []string{"-shutdown-timeout", "0s"}, 2, "Usage:"},
		{"negative timeout", []string{"-shutdown-timeout", "-1s"}, 2, "Usage:"},
		{"invalid timeout", []string{"-shutdown-timeout", "bad"}, 2, "invalid value"},
		{"empty listen", []string{"-listen", ""}, 2, "Usage:"},
		{"cancelled", []string{"-listen", "127.0.0.1:0"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var stdout, stderr bytes.Buffer
			if code := run(ctx, tc.args, &stdout, &stderr); code != tc.code {
				t.Fatalf("exit %d, want %d", code, tc.code)
			}
			if !strings.Contains(stderr.String(), tc.want) || stdout.Len() != 0 {
				t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestCLIReportsBindFailureAsJSON(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-listen", listener.Addr().String()}, &stdout, &stderr); code != 1 {
		t.Fatalf("unexpected exit %d", code)
	}
	var record map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["level"] != "ERROR" || record["component"] != "control" || record["phase"] != "failed" || record["error"] == nil || stderr.Len() != 0 {
		t.Fatalf("invalid failure report: %v", record)
	}
}

func TestJoinedShutdownFailureCannotBecomeSuccessfulExit(t *testing.T) {
	if !isCancellationOnly(context.Canceled) || !isCancellationOnly(errors.Join(context.Canceled)) {
		t.Fatal("cancellation should be orderly")
	}
	if isCancellationOnly(errors.Join(context.Canceled, context.DeadlineExceeded)) || isCancellationOnly(context.DeadlineExceeded) {
		t.Fatal("shutdown deadline must fail the process")
	}
	if isCancellationOnly(fmt.Errorf("shutdown: %w", errors.Join(context.Canceled, context.DeadlineExceeded))) {
		t.Fatal("nested shutdown failure must fail the process")
	}
}
