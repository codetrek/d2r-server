package guestlayout

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func syntheticGuest() ([]byte, string, []byte) {
	data := make([]byte, 0x1c000)
	u32, u64 := binary.LittleEndian.PutUint32, binary.LittleEndian.PutUint64
	u32(data[0:4], 0xfeedfacf)
	u32(data[4:8], 0x100000c)
	u32(data[12:16], 6)
	u32(data[16:20], 2)
	u32(data[20:24], 232+152)
	segment := func(at int, name string, address, size uint64, count, permissions uint32) {
		u32(data[at:at+4], 0x19)
		u32(data[at+4:at+8], 72+count*80)
		copy(data[at+8:at+24], name)
		u64(data[at+24:at+32], address)
		u64(data[at+32:at+40], size)
		u64(data[at+40:at+48], address)
		u64(data[at+48:at+56], size)
		u32(data[at+56:at+60], permissions)
		u32(data[at+60:at+64], permissions)
		u32(data[at+64:at+68], count)
	}
	section := func(at int, name, segment string, address, size uint64) {
		copy(data[at:at+16], name)
		copy(data[at+16:at+32], segment)
		u64(data[at+32:at+40], address)
		u64(data[at+40:at+48], size)
		u32(data[at+48:at+52], uint32(address))
		u32(data[at+52:at+56], 14)
	}
	segment(32, "__TEXT", 0x4000, 0x8000, 2, 5)
	section(104, "__dmmainx0", "__TEXT", 0x4000, 0x4000)
	section(184, "__dmmainx1", "__TEXT", 0x8000, 0x4000)
	segment(264, "__DMmainW", 0xc000, 0x10000, 1, 3)
	section(336, "__img", "__DMmainW", 0xc000, 0x10000)
	for index := 0x4000; index < len(data); index++ {
		data[index] = byte((index/0x4000)*37 + index%251)
	}
	fixups := "# PATCH_OFF\t0x14000\n# KEY_PAGE_OFF\t0xc000\n# KEY_PAGE_SIZE\t0x4000\n# TEXT_END\t0x3000\n# RO_OFF\t0x4000\n# DATA_OFF\t0x8000\n"
	for index, key := range requiredFixups[6:] {
		fixups += "# " + key + "\t" + string(rune('0'+index)) + "\n"
	}
	expected := append(bytes.Clone(data[0x4000:0x8000]), data[0xc000:0x1c000]...)
	expected = append(expected, data[0x8000:0xc000]...)
	return data, fixups, expected
}

func writeInputs(t *testing.T, data []byte, fixups string) Options {
	t.Helper()
	dir := t.TempDir()
	options := Options{GuestPath: filepath.Join(dir, "Guest"), FixupsPath: filepath.Join(dir, "main.fixups.tsv"), OutputDir: filepath.Join(dir, "result")}
	if err := os.WriteFile(options.GuestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(options.FixupsPath, []byte(fixups), 0o600); err != nil {
		t.Fatal(err)
	}
	return options
}

func TestInspectReconstructsLoaderOrder(t *testing.T) {
	data, fixups, expected := syntheticGuest()
	options := writeInputs(t, data, fixups)
	manifest, err := Inspect(options)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(options.OutputDir, ImageName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatal("reconstruction does not preserve section bytes in loader order")
	}
	digest := sha256.Sum256(expected)
	guestDigest := sha256.Sum256(data)
	if manifest.Reconstructed.SHA256 != hex.EncodeToString(digest[:]) || manifest.Guest.SHA256 != hex.EncodeToString(guestDigest[:]) || manifest.Reconstructed.Size != uint64(len(expected)) {
		t.Fatal("manifest provenance mismatch")
	}
	if manifest.Sections[1].Name != "__img" || manifest.Sections[1].ReconstructedStart != 0x4000 || manifest.Sections[2].ReconstructedStart != 0x14000 {
		t.Fatal("incorrect source-to-reconstructed mappings")
	}
	for _, section := range manifest.Sections {
		original := data[section.FileOffset : section.FileOffset+section.Size]
		digest := sha256.Sum256(original)
		if section.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("incorrect section provenance")
		}
	}
	stored, err := os.ReadFile(filepath.Join(options.OutputDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Manifest
	if err := json.Unmarshal(stored, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RelocationsApplied || decoded.RuntimeInitializationPerformed || decoded.CodeSignatureVerified || !decoded.SourceSectionsVerified || bytes.Contains(stored, []byte(filepath.Dir(options.GuestPath))) {
		t.Fatal("manifest claims unsupported work")
	}
	assertNoStaging(t, options.OutputDir)
}

func TestInspectRejectsMalformedGuestWithoutPublishing(t *testing.T) {
	cases := []struct {
		name string
		edit func([]byte) []byte
	}{
		{"truncated header", func(data []byte) []byte { return data[:12] }},
		{"fat binary", func(data []byte) []byte { binary.BigEndian.PutUint32(data[:4], 0xcafebabe); return data }},
		{"wrong architecture", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[4:8], 0x1000007); return data }},
		{"truncated payload", func(data []byte) []byte { return data[:len(data)-1] }},
		{"oversized commands", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[20:24], 0xffffffff); return data }},
		{"zero command size", func(data []byte) []byte { clear(data[36:40]); return data }},
		{"extra command padding", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[20:24], 232+152+8); return data }},
		{"invalid section count", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[96:100], 0xffffffff); return data }},
		{"missing section", func(data []byte) []byte {
			copy(data[104:120], []byte("__other\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
			return data
		}},
		{"duplicate section", func(data []byte) []byte { copy(data[184:200], data[104:120]); return data }},
		{"section outside segment", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[152:156], 0x18000); return data }},
		{"section memory overflow", func(data []byte) []byte { binary.LittleEndian.PutUint64(data[136:144], ^uint64(0)-10); return data }},
		{"section overlap", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[232:236], 0x4000); return data }},
		{"unaligned section", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[152:156], 0x4001); return data }},
		{"invalid alignment exponent", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[156:160], 64); return data }},
		{"zero fill section", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[168:172], 1); return data }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, fixups, _ := syntheticGuest()
			options := writeInputs(t, tc.edit(data), fixups)
			if _, err := Inspect(options); err == nil {
				t.Fatal("accepted malformed Guest")
			}
			assertUnpublished(t, options.OutputDir)
		})
	}
}

func TestInspectRejectsInvalidFixupsWithoutPublishing(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
	}{
		{"duplicate", func(s string) string { return s + "# TEXT_END 0x3000\n" }},
		{"missing", func(s string) string { return strings.ReplaceAll(s, "# DATA_OFF\t0x8000\n", "") }},
		{"unknown", func(s string) string { return s + "# UNKNOWN 1\n" }},
		{"invalid format", func(s string) string { return s + "0x12\tpatch\n" }},
		{"blank line", func(s string) string { return s + "\n" }},
		{"negative", func(s string) string { return strings.ReplaceAll(s, "0x3000", "-1") }},
		{"positive sign", func(s string) string { return strings.ReplaceAll(s, "0x3000", "+12288") }},
		{"ambiguous decimal boundary", func(s string) string { return strings.ReplaceAll(s, "0x3000", "12288") }},
		{"hexadecimal slot", func(s string) string { return strings.ReplaceAll(s, "KEY_KTLS_IDX\t1", "KEY_KTLS_IDX\t0x1") }},
		{"numeric overflow", func(s string) string { return strings.ReplaceAll(s, "0x3000", "0x10000000000000000") }},
		{"empty hex", func(s string) string { return strings.ReplaceAll(s, "0x3000", "0x") }},
		{"invalid hex", func(s string) string { return strings.ReplaceAll(s, "0x3000", "0xz00") }},
		{"text boundary mismatch", func(s string) string { return strings.ReplaceAll(s, "0x3000", "0x8000") }},
		{"patch boundary mismatch", func(s string) string { return strings.ReplaceAll(s, "0x14000", "0x10000") }},
		{"key page outside patch", func(s string) string { return strings.ReplaceAll(s, "0xc000", "0x14000") }},
		{"key page size", func(s string) string { return strings.ReplaceAll(s, "KEY_PAGE_SIZE\t0x4000", "KEY_PAGE_SIZE\t0x1000") }},
		{"key slot duplicate", func(s string) string { return strings.ReplaceAll(s, "KEY_KTLS_IDX\t1", "KEY_KTLS_IDX\t0") }},
		{"key slot outside page", func(s string) string { return strings.ReplaceAll(s, "KEY_KTLS_IDX\t1", "KEY_KTLS_IDX\t2048") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, fixups, _ := syntheticGuest()
			options := writeInputs(t, data, tc.edit(fixups))
			if _, err := Inspect(options); err == nil {
				t.Fatal("accepted invalid fixups")
			}
			assertUnpublished(t, options.OutputDir)
		})
	}
}

func TestInspectPreservesExistingOutput(t *testing.T) {
	for _, kind := range []string{"file", "empty directory", "nonempty directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			data, fixups, _ := syntheticGuest()
			options := writeInputs(t, data, fixups)
			switch kind {
			case "file":
				if err := os.WriteFile(options.OutputDir, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty directory", "nonempty directory":
				if err := os.Mkdir(options.OutputDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "nonempty directory" {
					if err := os.WriteFile(filepath.Join(options.OutputDir, "keep"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "symlink":
				if err := os.Symlink("missing", options.OutputDir); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(options.OutputDir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Inspect(options); !errors.Is(err, os.ErrExist) {
				t.Fatalf("expected output collision, got %v", err)
			}
			after, err := os.Lstat(options.OutputDir)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("existing output changed")
			}
			assertNoStaging(t, options.OutputDir)
		})
	}
}

func TestConcurrentPublicationHasExactlyOneWinner(t *testing.T) {
	data, fixups, expected := syntheticGuest()
	options := writeInputs(t, data, fixups)
	const count = 8
	results := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Go(func() { _, err := Inspect(options); results <- err })
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successful publishers", successes)
	}
	image, err := os.ReadFile(filepath.Join(options.OutputDir, ImageName))
	if err != nil || !bytes.Equal(image, expected) {
		t.Fatal("published image is incomplete")
	}
	assertNoStaging(t, options.OutputDir)
}

func TestPublishDoesNotReplaceDirectoryCreatedAfterCheck(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "staging"), filepath.Join(dir, "result")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishDirectory(source, destination); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected exclusive rename collision, got %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("staging source lost")
	}
}

func TestWriteImagePropagatesReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image")
	_, err := writeImage(path, bytes.NewReader(nil), []Section{{Name: "test", Size: 1}}, map[uint64]string{})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected original read error, got %v", err)
	}
}

func assertNoStaging(t *testing.T, out string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(out), ".d2r-inspect-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("owned staging was left behind: %v %v", matches, err)
	}
}

func assertUnpublished(t *testing.T, out string) {
	t.Helper()
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partially published output: %v", err)
	}
	assertNoStaging(t, out)
}
