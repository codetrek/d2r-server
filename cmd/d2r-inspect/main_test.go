package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codetrek/d2r-server/internal/guestlayout"
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
		{"empty module", []string{"-module", "", "-guest", "missing", "-fixups", "missing", "-out", t.TempDir() + "/new"}, 1, "module must be explicitly set"},
		{"unsupported module", []string{"-module", "other", "-guest", "missing", "-fixups", "missing", "-out", t.TempDir() + "/new"}, 1, "module must be explicitly set"},
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

func TestCLIModuleSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		moduleFlag []string
		module     guestlayout.Module
	}{
		{"default main", nil, guestlayout.ModuleMain},
		{"explicit main", []string{"-module", "main"}, guestlayout.ModuleMain},
		{"explicit sdk", []string{"-module", "sdk"}, guestlayout.ModuleSDK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			guest, fixups := syntheticCLIInputs(tc.module)
			guestPath, fixupsPath := filepath.Join(dir, "Guest"), filepath.Join(dir, string(tc.module)+".fixups.tsv")
			if err := os.WriteFile(guestPath, guest, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixupsPath, []byte(fixups), 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "result")
			args := append([]string{"-guest", guestPath, "-fixups", fixupsPath, "-out", out}, tc.moduleFlag...)
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, &stderr)
			}
			var manifest guestlayout.Manifest
			if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Module != tc.module || manifest.SchemaVersion != 2 || manifest.LayoutProfile != "dm-split-sections-v1" || manifest.Reconstructed.Name != string(tc.module)+".patched.reconstructed.bin" {
				t.Fatalf("incorrect module identity: %+v", manifest)
			}
			if _, err := os.Stat(filepath.Join(out, manifest.Reconstructed.Name)); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(stdout.Bytes(), []byte(dir)) || stderr.Len() != 0 {
				t.Fatal("CLI metadata leaked local paths or wrote diagnostics on success")
			}
		})
	}
}

func syntheticCLIInputs(module guestlayout.Module) ([]byte, string) {
	data := make([]byte, 0x1c000)
	u32, u64 := binary.LittleEndian.PutUint32, binary.LittleEndian.PutUint64
	u32(data, 0xfeedfacf)
	u32(data[4:], 0x0100000c)
	u32(data[12:], 6)
	u32(data[16:], 2)
	u32(data[20:], 384)
	segment := func(at int, name string, offset, size uint64, sections uint32) {
		u32(data[at:], 0x19)
		u32(data[at+4:], 72+sections*80)
		copy(data[at+8:at+24], name)
		u64(data[at+24:], offset)
		u64(data[at+32:], size)
		u64(data[at+40:], offset)
		u64(data[at+48:], size)
		u32(data[at+64:], sections)
	}
	section := func(at int, name, segment string, offset, size uint64) {
		copy(data[at:at+16], name)
		copy(data[at+16:at+32], segment)
		u64(data[at+32:], offset)
		u64(data[at+40:], size)
		u32(data[at+48:], uint32(offset))
		u32(data[at+52:], 14)
	}
	segment(32, "__TEXT", 0x4000, 0x8000, 2)
	section(104, "__dm"+string(module)+"x0", "__TEXT", 0x4000, 0x4000)
	section(184, "__dm"+string(module)+"x1", "__TEXT", 0x8000, 0x4000)
	segment(264, "__DM"+string(module)+"W", 0xc000, 0x10000, 1)
	section(336, "__img", "__DM"+string(module)+"W", 0xc000, 0x10000)
	return data, "# PATCH_OFF 0x14000\n# KEY_PAGE_OFF 0xc000\n# KEY_PAGE_SIZE 0x4000\n# TEXT_END 0x3000\n# RO_OFF 0x4000\n# DATA_OFF 0x8000\n# KEY_K18_IDX 0\n# KEY_KTLS_IDX 1\n# KEY_KTLR_IDX 2\n# KEY_KGATE_IDX 3\n# KEY_KRES0_IDX 4\n# KEY_KRES1_IDX 5\n# KEY_KASPECT_IDX 6\n# KEY_KASPECTUI_IDX 7\n"
}
