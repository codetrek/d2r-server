// Package guestlayout validates and reconstructs an analysis-only Guest image.
package guestlayout

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

const ImageName = "main.patched.reconstructed.bin"

type Options struct {
	GuestPath  string
	FixupsPath string
	OutputDir  string
}

type Artifact struct {
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion                  int               `json:"schema_version"`
	Generator                      string            `json:"generator"`
	Purpose                        string            `json:"purpose"`
	Guest                          Artifact          `json:"input_guest"`
	Fixups                         Artifact          `json:"input_fixups"`
	FixupValues                    map[string]uint64 `json:"fixup_values"`
	Reconstructed                  Artifact          `json:"reconstructed"`
	Sections                       []Section         `json:"source_to_reconstructed_ranges"`
	SourceSectionsVerified         bool              `json:"source_sections_verified"`
	CodeSignatureVerified          bool              `json:"code_signature_verified"`
	RelocationsApplied             bool              `json:"relocations_applied"`
	RuntimeInitializationPerformed bool              `json:"runtime_initialization_performed"`
	Limits                         []string          `json:"limits"`
}

// Inspect publishes a complete new directory with no replacement of an existing
// destination. Only the directory created by this invocation is cleaned up.
func Inspect(options Options) (_ *Manifest, resultErr error) {
	if options.GuestPath == "" || options.FixupsPath == "" || options.OutputDir == "" {
		return nil, fmt.Errorf("guest, fixups and output directory paths are required")
	}
	out, err := filepath.Abs(options.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve output directory: %w", err)
	}
	if _, err := os.Lstat(out); err == nil {
		return nil, fmt.Errorf("output directory already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("check output directory: %w", err)
	}
	guest, err := os.Open(options.GuestPath)
	if err != nil {
		return nil, fmt.Errorf("open Guest: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, guest.Close()) }()
	guestInfo, err := guest.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat Guest: %w", err)
	}
	if !guestInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("Guest must be a regular file")
	}
	sections, err := readMachO(guest, uint64(guestInfo.Size()))
	if err != nil {
		return nil, err
	}
	fixupBytes, err := os.ReadFile(options.FixupsPath)
	if err != nil {
		return nil, fmt.Errorf("read fixups file: %w", err)
	}
	values, err := readFixups(bytes.NewReader(fixupBytes))
	if err != nil {
		return nil, err
	}
	total, err := validateLayout(sections, values)
	if err != nil {
		return nil, err
	}
	guestDigest, expectedSectionDigests, err := hashSource(guest, uint64(guestInfo.Size()), sections)
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(out), ".d2r-inspect-*")
	if err != nil {
		return nil, fmt.Errorf("create output staging directory: %w", err)
	}
	defer func() {
		if staging != "" {
			if err := os.RemoveAll(staging); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove owned staging directory: %w", err))
			}
		}
	}()
	image, err := writeImage(filepath.Join(staging, ImageName), guest, sections, expectedSectionDigests)
	if err != nil {
		return nil, err
	}
	if image.Size != total {
		return nil, fmt.Errorf("reconstructed image length mismatch")
	}
	finalInfo, err := guest.Stat()
	if err != nil {
		return nil, fmt.Errorf("recheck Guest: %w", err)
	}
	if finalInfo.Size() != guestInfo.Size() || !finalInfo.ModTime().Equal(guestInfo.ModTime()) {
		return nil, fmt.Errorf("Guest changed during inspection")
	}
	fixupDigest := sha256.Sum256(fixupBytes)
	manifest := &Manifest{
		SchemaVersion: 1, Generator: "github.com/codetrek/d2r-server/cmd/d2r-inspect",
		Purpose:     "Static reconstruction of patched main bytes in loader order; not a runnable server or executable.",
		Guest:       Artifact{Name: filepath.Base(options.GuestPath), Size: uint64(guestInfo.Size()), SHA256: guestDigest},
		Fixups:      Artifact{Name: filepath.Base(options.FixupsPath), Size: uint64(len(fixupBytes)), SHA256: hex.EncodeToString(fixupDigest[:])},
		FixupValues: values, Reconstructed: image, Sections: sections, SourceSectionsVerified: true,
		Limits: []string{
			"The Mach-O container is parsed, but its Apple code signature is not authenticated.",
			"Guest bytes are copied unchanged; relocations, imports, TLS, constructors and host services are not initialized.",
			"This patched iOS layout does not establish Linux compatibility or multiplayer capability.",
		},
	}
	if err := writeManifest(filepath.Join(staging, "manifest.json"), manifest); err != nil {
		return nil, err
	}
	if err := syncDirectory(staging); err != nil {
		return nil, fmt.Errorf("sync staging directory: %w", err)
	}
	if err := publishDirectory(staging, out); err != nil {
		return nil, fmt.Errorf("publish output directory: %w", err)
	}
	staging = ""
	return manifest, nil
}

func hashSource(source io.ReaderAt, size uint64, sections []Section) (string, map[uint64]string, error) {
	ordered := slices.Clone(sections)
	slices.SortFunc(ordered, func(a, b Section) int {
		if a.FileOffset < b.FileOffset {
			return -1
		}
		if a.FileOffset > b.FileOffset {
			return 1
		}
		return 0
	})
	whole := sha256.New()
	digests := make(map[uint64]string, len(sections))
	var position uint64
	for _, section := range ordered {
		if _, err := io.CopyN(whole, io.NewSectionReader(source, int64(position), int64(section.FileOffset-position)), int64(section.FileOffset-position)); err != nil {
			return "", nil, fmt.Errorf("hash Guest before section %s: %w", section.Name, err)
		}
		sectionHash := sha256.New()
		if _, err := io.CopyN(io.MultiWriter(whole, sectionHash), io.NewSectionReader(source, int64(section.FileOffset), int64(section.Size)), int64(section.Size)); err != nil {
			return "", nil, fmt.Errorf("hash Guest section %s: %w", section.Name, err)
		}
		digests[section.FileOffset] = hex.EncodeToString(sectionHash.Sum(nil))
		position = section.FileOffset + section.Size
	}
	if _, err := io.CopyN(whole, io.NewSectionReader(source, int64(position), int64(size-position)), int64(size-position)); err != nil {
		return "", nil, fmt.Errorf("hash Guest tail: %w", err)
	}
	return hex.EncodeToString(whole.Sum(nil)), digests, nil
}

func writeImage(path string, source io.ReaderAt, sections []Section, expected map[uint64]string) (_ Artifact, resultErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Artifact{}, fmt.Errorf("create reconstructed image: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	imageHash := sha256.New()
	var size uint64
	for index, section := range sections {
		sectionHash := sha256.New()
		if _, err := io.CopyN(io.MultiWriter(file, imageHash, sectionHash), io.NewSectionReader(source, int64(section.FileOffset), int64(section.Size)), int64(section.Size)); err != nil {
			return Artifact{}, fmt.Errorf("copy Guest section %s: %w", section.Name, err)
		}
		sections[index].SHA256 = hex.EncodeToString(sectionHash.Sum(nil))
		if sections[index].SHA256 != expected[section.FileOffset] {
			return Artifact{}, fmt.Errorf("Guest section %s changed during inspection", section.Name)
		}
		size += section.Size
	}
	if err := file.Sync(); err != nil {
		return Artifact{}, fmt.Errorf("sync reconstructed image: %w", err)
	}
	return Artifact{Name: ImageName, Size: size, SHA256: hex.EncodeToString(imageHash.Sum(nil))}, nil
}

func writeManifest(path string, manifest *Manifest) (resultErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create manifest: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync manifest: %w", err)
	}
	return nil
}

func syncDirectory(path string) (resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	return file.Sync()
}
