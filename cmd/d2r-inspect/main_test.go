package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLIUsageAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"missing arguments", nil, 2, "Usage:"},
		{"help", []string{"-h"}, 0, "Usage:"},
		{"unknown flag", []string{"-unknown"}, 2, "flag provided but not defined"},
		{"extra argument", []string{"unexpected"}, 2, "Usage:"},
		{"missing Guest", []string{"-guest", "missing", "-fixups", "missing", "-out", t.TempDir() + "/new"}, 1, "open Guest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, &stdout, &stderr); code != tc.code {
				t.Fatalf("got exit %d, want %d", code, tc.code)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("unexpected channels: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}
