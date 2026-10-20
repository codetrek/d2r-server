package guestlayout

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectRequiresExplicitSupportedModule(t *testing.T) {
	for _, module := range []Module{"", "other", "SDK", "../sdk"} {
		t.Run(string(module), func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "result")
			if _, err := Inspect(Options{Module: module, GuestPath: "missing", FixupsPath: "missing", OutputDir: output}); err == nil || !strings.Contains(err.Error(), "module must be explicitly set") {
				t.Fatalf("expected module validation before file access, got %v", err)
			}
			assertUnpublished(t, output)
		})
	}
}

func TestInspectRejectsOtherModulesSections(t *testing.T) {
	for _, selected := range []Module{ModuleMain, ModuleSDK} {
		t.Run(string(selected), func(t *testing.T) {
			other := ModuleSDK
			if selected == ModuleSDK {
				other = ModuleMain
			}
			data, fixups, _ := syntheticGuest(other)
			options := writeInputs(t, selected, data, fixups)
			if _, err := Inspect(options); err == nil || !strings.Contains(err.Error(), "missing Mach-O section __TEXT,__dm"+string(selected)+"x0") {
				t.Fatalf("expected exact selected module sections, got %v", err)
			}
			assertUnpublished(t, options.OutputDir)
		})
	}
}

func TestSDKValidationAndPublication(t *testing.T) {
	for _, kind := range []string{"missing section", "truncated", "fixup mismatch", "output collision"} {
		t.Run(kind, func(t *testing.T) {
			data, fixups, _ := syntheticGuest(ModuleSDK)
			switch kind {
			case "missing section":
				clear(data[184:200])
				copy(data[184:200], "__dmmainx1")
			case "truncated":
				data = data[:len(data)-1]
			case "fixup mismatch":
				fixups = strings.ReplaceAll(fixups, "0x14000", "0x10000")
			}
			options := writeInputs(t, ModuleSDK, data, fixups)
			if kind == "output collision" {
				if err := os.Mkdir(options.OutputDir, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(options.OutputDir, "keep")
				if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := Inspect(options); !errors.Is(err, os.ErrExist) {
					t.Fatalf("expected existing output error, got %v", err)
				}
				stored, err := os.ReadFile(marker)
				if err != nil || !bytes.Equal(stored, []byte("unchanged")) {
					t.Fatal("existing output was modified")
				}
				assertNoStaging(t, options.OutputDir)
				return
			}
			if _, err := Inspect(options); err == nil {
				t.Fatal("accepted invalid SDK layout")
			}
			assertUnpublished(t, options.OutputDir)
		})
	}
}
