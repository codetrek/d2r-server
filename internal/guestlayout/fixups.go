package guestlayout

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var requiredFixups = []string{
	"PATCH_OFF", "KEY_PAGE_OFF", "KEY_PAGE_SIZE", "TEXT_END", "RO_OFF", "DATA_OFF",
	"KEY_K18_IDX", "KEY_KTLS_IDX", "KEY_KTLR_IDX", "KEY_KGATE_IDX",
	"KEY_KRES0_IDX", "KEY_KRES1_IDX", "KEY_KASPECT_IDX", "KEY_KASPECTUI_IDX",
}

func readFixups(r io.Reader) (map[string]uint64, error) {
	values := make(map[string]uint64, len(requiredFixups))
	known := make(map[string]bool, len(requiredFixups))
	for _, key := range requiredFixups {
		known[key] = true
	}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[0] != "#" {
			return nil, fmt.Errorf("fixups line %d: expected '# KEY VALUE'", line)
		}
		key, literal := fields[1], fields[2]
		if !known[key] {
			return nil, fmt.Errorf("fixups line %d: unsupported key %q", line, key)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("fixups line %d: duplicate key %q", line, key)
		}
		base := 10
		hexadecimal := strings.HasPrefix(literal, "0x") || strings.HasPrefix(literal, "0X")
		if strings.HasSuffix(key, "_IDX") {
			if hexadecimal {
				return nil, fmt.Errorf("fixups line %d: %s must use decimal slot syntax", line, key)
			}
		} else if !hexadecimal {
			return nil, fmt.Errorf("fixups line %d: %s must use 0x-prefixed boundary syntax", line, key)
		}
		if hexadecimal {
			literal, base = literal[2:], 16
		}
		for _, digit := range literal {
			if !(digit >= '0' && digit <= '9' || base == 16 && (digit >= 'a' && digit <= 'f' || digit >= 'A' && digit <= 'F')) {
				return nil, fmt.Errorf("fixups line %d: invalid unsigned integer %q", line, fields[2])
			}
		}
		value, err := strconv.ParseUint(literal, base, 64)
		if err != nil {
			return nil, fmt.Errorf("fixups line %d: parse %s: %w", line, key, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read fixups: %w", err)
	}
	for _, key := range requiredFixups {
		if _, exists := values[key]; !exists {
			return nil, fmt.Errorf("fixups: missing %s", key)
		}
	}
	return values, nil
}
