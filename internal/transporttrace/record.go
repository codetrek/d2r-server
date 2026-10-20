package transporttrace

import (
	"encoding/binary"
	"fmt"

	"github.com/codetrek/d2r-server/internal/guestingress"
)

type record struct {
	size                    uint32
	kind, flags             uint16
	sequence, frame, thread uint64
	clientID, mode, length  uint32
	original                uint64
}

func decodeRecord(b []byte, h header, sequence, offset uint64) (record, error) {
	r := record{size: binary.LittleEndian.Uint32(b), kind: binary.LittleEndian.Uint16(b[4:]), flags: binary.LittleEndian.Uint16(b[6:]), sequence: binary.LittleEndian.Uint64(b[8:]), frame: binary.LittleEndian.Uint64(b[16:]), thread: binary.LittleEndian.Uint64(b[24:]), clientID: binary.LittleEndian.Uint32(b[32:]), mode: binary.LittleEndian.Uint32(b[36:]), length: binary.LittleEndian.Uint32(b[40:]), original: binary.LittleEndian.Uint64(b[48:])}
	bad := func(reason string) (record, error) {
		return r, fmt.Errorf("record %d at %d: %s", sequence, offset, reason)
	}
	if r.kind < 1 || r.kind > 4 {
		return bad("unknown kind")
	}
	if r.sequence != sequence {
		return bad("noncontiguous sequence")
	}
	if r.thread != h.owner {
		return bad("record thread differs from owner")
	}
	if binary.LittleEndian.Uint32(b[44:]) != 0 || binary.LittleEndian.Uint64(b[56:]) != 0 {
		return bad("nonzero reserved field")
	}
	if r.flags & ^uint16(payloadFlag|ignoredFlag|emptyFlag|badSpanFlag|capExceededFlag|threadViolationFlag|recordGapFlag|clientIDFlag) != 0 {
		return bad("unknown flags")
	}
	if r.length > uint32(h.observationCap) {
		return bad("payload exceeds observation cap")
	}
	expected := (uint64(RecordHeaderSize) + uint64(r.length) + 7) &^ uint64(7)
	if uint64(r.size) != expected {
		return bad("record size differs from aligned payload extent")
	}
	if r.flags&clientIDFlag == 0 && r.clientID != 0 {
		return bad("client ID present without valid flag")
	}
	if r.kind >= 3 && r.mode != 0 {
		return bad("postpop mode must be zero")
	}
	if r.flags&payloadFlag != 0 {
		if r.flags & ^uint16(payloadFlag|clientIDFlag) != 0 || r.original != uint64(r.length) {
			return bad("inconsistent copied payload flags or original length")
		}
		if r.kind >= 3 && (r.length < 4 || r.flags&clientIDFlag == 0) {
			return bad("postpop payload lacks connection prefix")
		}
		if r.kind == 1 && (r.flags&clientIDFlag == 0 || r.clientID != 0 || r.mode != 1) {
			return bad("server send payload contradicts fixed local guards")
		}
		if r.kind == 2 && (r.flags&clientIDFlag != 0 || r.mode != 1) {
			return bad("client send payload contradicts fixed local guards")
		}
		return r, nil
	}
	if r.length != 0 {
		return bad("payload bytes present without payload flag")
	}
	if r.flags&emptyFlag != 0 {
		if r.kind < 3 || r.flags != emptyFlag || r.original != 0 {
			return bad("invalid empty postpop")
		}
		return r, nil
	}
	if r.flags&ignoredFlag != 0 {
		if r.kind >= 3 || r.original != 0 {
			return bad("invalid ignored send attempt")
		}
		if r.kind == 1 && r.flags != ignoredFlag|clientIDFlag {
			return bad("ignored server attempt lacks connection metadata")
		}
		if r.kind == 2 && (r.flags != ignoredFlag || r.mode == 1 || r.mode == 3) {
			return bad("ignored client attempt contradicts fixed mode guards")
		}
		return r, nil
	}
	errors := r.flags & uint16(badSpanFlag|capExceededFlag|threadViolationFlag|recordGapFlag)
	if errors == 0 {
		return bad("record has neither observed payload nor declared outcome")
	}
	if r.flags&capExceededFlag != 0 && r.original <= h.observationCap {
		return bad("cap-exceeded record has no oversized original length")
	}
	if r.kind == 2 && r.flags&clientIDFlag != 0 {
		return bad("client send error has unsupported connection metadata")
	}
	return r, nil
}

func observe(k *summary, r record, payload []byte) error {
	k.value.Records++
	if k.value.Records == 1 {
		k.value.FirstFrame = r.frame
	}
	k.value.LastFrame = r.frame
	if r.flags&ignoredFlag != 0 {
		k.value.Ignored++
	}
	if r.flags&emptyFlag != 0 {
		k.value.Empty++
	}
	if r.flags&(badSpanFlag|capExceededFlag|threadViolationFlag|recordGapFlag) != 0 {
		k.value.ErrorRecords++
	}
	if r.flags&payloadFlag == 0 {
		return nil
	}
	k.value.PayloadRecords++
	k.value.CopiedBytes += uint64(len(payload))
	_, _ = k.copied.Write(payload)
	if r.kind >= 3 {
		message, err := guestingress.DecodeReceiverBuffer(payload, uint32(len(payload)-4))
		if err != nil {
			return fmt.Errorf("record %d receiver buffer: %w", r.sequence, err)
		}
		if message.ClientID != r.clientID {
			return fmt.Errorf("record %d client ID differs from copied receiver prefix", r.sequence)
		}
		_, _ = k.guest.Write(message.Payload)
	}
	return nil
}

func validateCounts(h header, records, attempts, pops, empty, errorRecords uint64) error {
	if h.counts.Records < records || h.counts.SendAttempts < attempts || h.counts.PopCalls < pops || h.counts.EmptyPops < empty || h.counts.Errors < errorRecords {
		return fmt.Errorf("native counters smaller than committed observations")
	}
	if h.flags&closed == 0 && h.counts.Records-records > 1 {
		return fmt.Errorf("unclosed trace counter leads committed records by more than one")
	}
	if h.flags&closed != 0 && h.counts.Records != records {
		return fmt.Errorf("closed trace record count differs from committed records")
	}
	if h.flags&closed != 0 && h.flags&captureGap == 0 && (h.counts.SendAttempts != attempts || h.counts.PopCalls != pops || h.counts.EmptyPops != empty) {
		return fmt.Errorf("closed gap-free trace counters differ from committed observations")
	}
	return nil
}
