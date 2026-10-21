package lifecycletrace

import (
	"encoding/binary"
	"fmt"
)

type record struct {
	sequence, frame, monotonicNS, transportSequence        uint64
	gate, phase, flags, operationMode                      uint32
	stateB90, stateB94, stateB98, errorCode, transportMode uint32
	timer1F0, timer1F4                                     uint32
	errorDeadline                                          uint64
}

func decodeRecord(b []byte, sequence uint64) (record, error) {
	u32 := func(at int) uint32 { return binary.LittleEndian.Uint32(b[at:]) }
	u64 := func(at int) uint64 { return binary.LittleEndian.Uint64(b[at:]) }
	x := record{sequence: u64(0), frame: u64(8), monotonicNS: u64(16), transportSequence: u64(24), gate: u32(32), phase: u32(36), flags: u32(56), operationMode: u32(60),
		stateB90: u32(64), stateB94: u32(68), stateB98: u32(72), errorCode: u32(84), timer1F0: u32(88), timer1F4: u32(92), transportMode: u32(100), errorDeadline: u64(128)}
	bad := func(reason string) (record, error) { return x, fmt.Errorf("lifecycle record %d: %s", sequence, reason) }
	if x.sequence != sequence || (x.gate != 1 && x.gate != 2) || x.phase > 1 {
		return bad("invalid sequence, gate or phase")
	}
	if x.flags & ^uint32(uiMetadata|uiUnreadable|parentPresent|parentState|parentUnreadable) != 0 || u32(124) != 0 || !zero(b[142:]) {
		return bad("unknown flags or nonzero reserved bytes")
	}
	for _, v := range b[136:142] {
		if v > 1 {
			return bad("nonboolean snapshot field")
		}
	}
	ui := x.flags & (uiMetadata | uiUnreadable)
	if x.gate == 1 && ui != uiMetadata && ui != uiUnreadable {
		return bad("UI snapshot must declare metadata or unreadable")
	}
	if x.gate == 2 && ui != 0 {
		return bad("tick snapshot has UI flags")
	}
	if ui != uiMetadata && (u64(40) != 0 || u64(48) != 0) {
		return bad("UI hashes present without metadata")
	}
	parent := x.flags & (parentPresent | parentState | parentUnreadable)
	if parent != 0 && parent != parentPresent|parentState && parent != parentPresent|parentUnreadable {
		return bad("inconsistent parent observation flags")
	}
	if parent != parentPresent|parentState && u32(120) != 0 {
		return bad("parent state present without readable flag")
	}
	return x, nil
}
