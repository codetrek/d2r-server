package guestlayout

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const pageSize = uint64(0x4000)

type Section struct {
	Segment            string `json:"segment"`
	Name               string `json:"section"`
	Address            uint64 `json:"address"`
	Size               uint64 `json:"size"`
	FileOffset         uint64 `json:"file_offset"`
	Alignment          uint32 `json:"alignment_exponent"`
	MappingPermissions uint32 `json:"signed_mapping_permissions"`
	ReconstructedStart uint64 `json:"reconstructed_start"`
	SHA256             string `json:"sha256"`
}

func readMachO(r io.ReaderAt, fileSize uint64, profile layoutProfile) ([]Section, error) {
	var header [32]byte
	if _, err := r.ReadAt(header[:], 0); err != nil {
		return nil, fmt.Errorf("read Mach-O header: %w", err)
	}
	u32 := binary.LittleEndian.Uint32
	if u32(header[:4]) != 0xfeedfacf || u32(header[4:8]) != 0x0100000c {
		return nil, fmt.Errorf("expected thin little-endian ARM64 Mach-O")
	}
	ncmds, sizeofcmds := uint64(u32(header[16:20])), uint64(u32(header[20:24]))
	if !within(32, sizeofcmds, fileSize) || ncmds > sizeofcmds/8 {
		return nil, fmt.Errorf("Mach-O load commands exceed file bounds")
	}
	commandEnd, position := uint64(32)+sizeofcmds, uint64(32)
	selected := make(map[[2]string]Section, 3)
	for index := uint64(0); index < ncmds; index++ {
		var command [8]byte
		if !within(position, 8, commandEnd) {
			return nil, fmt.Errorf("Mach-O load command %d: truncated header", index)
		}
		if _, err := r.ReadAt(command[:], int64(position)); err != nil {
			return nil, fmt.Errorf("read load command %d: %w", index, err)
		}
		kind, size := u32(command[:4]), uint64(u32(command[4:8]))
		if size < 8 || size%8 != 0 || !within(position, size, commandEnd) {
			return nil, fmt.Errorf("Mach-O load command %d: invalid size %d", index, size)
		}
		if kind == 0x19 {
			if err := readSegment(r, position, size, fileSize, profile, selected); err != nil {
				return nil, fmt.Errorf("Mach-O load command %d: %w", index, err)
			}
		}
		position += size
	}
	if position != commandEnd {
		return nil, fmt.Errorf("Mach-O load command sizes do not equal sizeofcmds")
	}
	sections := make([]Section, 0, len(profile.sectionNames))
	for _, name := range profile.sectionNames {
		section, exists := selected[name]
		if !exists {
			return nil, fmt.Errorf("missing Mach-O section %s,%s", name[0], name[1])
		}
		if section.FileOffset < commandEnd {
			return nil, fmt.Errorf("layout section %s overlaps Mach-O headers", section.Name)
		}
		sections = append(sections, section)
	}
	for i, a := range sections {
		for _, b := range sections[i+1:] {
			if a.FileOffset < b.FileOffset+b.Size && b.FileOffset < a.FileOffset+a.Size {
				return nil, fmt.Errorf("selected Mach-O section file ranges overlap")
			}
			if a.Address < b.Address+b.Size && b.Address < a.Address+a.Size {
				return nil, fmt.Errorf("selected Mach-O section address ranges overlap")
			}
		}
	}
	return sections, nil
}

func readSegment(r io.ReaderAt, position, commandSize, fileSize uint64, profile layoutProfile, selected map[[2]string]Section) error {
	if commandSize < 72 {
		return fmt.Errorf("truncated LC_SEGMENT_64")
	}
	var segment [72]byte
	if _, err := r.ReadAt(segment[:], int64(position)); err != nil {
		return fmt.Errorf("read segment: %w", err)
	}
	u32, u64 := binary.LittleEndian.Uint32, binary.LittleEndian.Uint64
	name := machoName(segment[8:24])
	address, memorySize := u64(segment[24:32]), u64(segment[32:40])
	offset, size := u64(segment[40:48]), u64(segment[48:56])
	if !within(offset, size, fileSize) || memorySize > ^uint64(0)-address {
		return fmt.Errorf("segment %s: invalid file or memory bounds", name)
	}
	count := uint64(u32(segment[64:68]))
	if count > (commandSize-72)/80 || commandSize != 72+count*80 {
		return fmt.Errorf("segment %s: invalid section count", name)
	}
	for index := uint64(0); index < count; index++ {
		var raw [80]byte
		if _, err := r.ReadAt(raw[:], int64(position+72+index*80)); err != nil {
			return fmt.Errorf("read section: %w", err)
		}
		sectionName, segmentName := machoName(raw[:16]), machoName(raw[16:32])
		key := [2]string{segmentName, sectionName}
		wanted := false
		for _, candidate := range profile.sectionNames {
			wanted = wanted || key == candidate
		}
		if !wanted {
			continue
		}
		if _, exists := selected[key]; exists {
			return fmt.Errorf("duplicate layout section %s,%s", segmentName, sectionName)
		}
		section := Section{Segment: segmentName, Name: sectionName, Address: u64(raw[32:40]), Size: u64(raw[40:48]), FileOffset: uint64(u32(raw[48:52])), Alignment: u32(raw[52:56]), MappingPermissions: u32(segment[60:64])}
		if segmentName != name || section.Size == 0 || !within(section.FileOffset, section.Size, fileSize) ||
			section.FileOffset < offset || !within(section.FileOffset-offset, section.Size, size) ||
			section.Address < address || !within(section.Address-address, section.Size, memorySize) {
			return fmt.Errorf("section %s,%s: invalid segment or file bounds", segmentName, sectionName)
		}
		if u32(raw[64:68])&0xff != 0 {
			return fmt.Errorf("section %s,%s: expected file-backed regular section", segmentName, sectionName)
		}
		if section.Alignment >= 64 || section.Address%(uint64(1)<<section.Alignment) != 0 || section.FileOffset%(uint64(1)<<section.Alignment) != 0 ||
			section.Address%pageSize != 0 || section.FileOffset%pageSize != 0 || section.Size%pageSize != 0 {
			return fmt.Errorf("section %s,%s: invalid alignment", segmentName, sectionName)
		}
		selected[key] = section
	}
	return nil
}

func machoName(raw []byte) string {
	if end := bytes.IndexByte(raw, 0); end >= 0 {
		raw = raw[:end]
	}
	return string(raw)
}

func within(start, size, limit uint64) bool {
	return start <= limit && size <= limit-start
}

func validateLayout(sections []Section, values map[string]uint64) (uint64, error) {
	var total uint64
	for index := range sections {
		sections[index].ReconstructedStart = total
		if sections[index].Size > uint64(1<<63-1)-total {
			return 0, fmt.Errorf("reconstructed image exceeds signed 64-bit file bounds")
		}
		total += sections[index].Size
	}
	text, ro, data := values["TEXT_END"], values["RO_OFF"], values["DATA_OFF"]
	key, keySize, patch := values["KEY_PAGE_OFF"], values["KEY_PAGE_SIZE"], values["PATCH_OFF"]
	if keySize != pageSize || key%pageSize != 0 || patch%pageSize != 0 || ro%0x1000 != 0 || data%0x1000 != 0 || text%4 != 0 {
		return 0, fmt.Errorf("fixup boundaries violate page or instruction alignment")
	}
	if !(text > 0 && text <= ro && ro < data && data < key && key < patch && keySize <= patch-key && patch < total) {
		return 0, fmt.Errorf("expected 0 < TEXT_END <= RO_OFF < DATA_OFF < KEY_PAGE_OFF < KEY_PAGE_END <= PATCH_OFF < image size")
	}
	if text > ^uint64(0)-(pageSize-1) || sections[0].Size != (text+pageSize-1)&^(pageSize-1) || sections[0].Size+sections[1].Size != patch {
		return 0, fmt.Errorf("fixups do not match loader section boundaries")
	}
	slots := make(map[uint64]string, 8)
	for _, name := range requiredFixups[6:] {
		value := values[name]
		if value >= keySize/8 {
			return 0, fmt.Errorf("%s exceeds key page slots", name)
		}
		if previous, exists := slots[value]; exists {
			return 0, fmt.Errorf("%s and %s reuse key page slot %d", previous, name, value)
		}
		slots[value] = name
	}
	return total, nil
}
