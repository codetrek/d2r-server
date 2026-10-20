// Package transporttrace reads stopped captures from the fixed Darwin ARM64
// transport observer. Its records describe local observations, not wire frames.
package transporttrace

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

const (
	HeaderSize          = 256
	RecordHeaderSize    = 64
	Capacity            = 64 * 1024 * 1024
	ObservationCap      = 1024 * 1024
	ProfileName         = "d2r-mobile-120fe55-darwin-arm64-transport-v2"
	mainSHA256          = "30384eabe394cbc6b6a0b7f3788983d58dc14a92b7abe1638853dc4833cf81f1"
	sdkSHA256           = "d48f2cc33f3e506d623f873ba447c88fe0aea989b98c4057e2481b6ca3988fa2"
	installed           = 1
	profileMatched      = 2
	closed              = 4
	captureGap          = 8
	payloadFlag         = 1
	ignoredFlag         = 2
	emptyFlag           = 4
	badSpanFlag         = 8
	capExceededFlag     = 16
	threadViolationFlag = 32
	recordGapFlag       = 64
	clientIDFlag        = 128
)

var kindNames = [...]string{"server_send_attempt", "client_send_attempt", "system_postpop", "gameplay_postpop", "client_high_postpop", "client_low_postpop"}

type Counters struct {
	Records          uint64 `json:"records"`
	SendAttempts     uint64 `json:"send_attempts"`
	PopCalls         uint64 `json:"pop_calls"`
	EmptyPops        uint64 `json:"empty_pops"`
	Errors           uint64 `json:"errors"`
	Overflow         uint64 `json:"overflow"`
	ThreadViolations uint64 `json:"thread_violations"`
}

type KindSummary struct {
	Kind                string `json:"kind"`
	Records             uint64 `json:"records"`
	PayloadRecords      uint64 `json:"payload_records"`
	Ignored             uint64 `json:"ignored"`
	Empty               uint64 `json:"empty"`
	ErrorRecords        uint64 `json:"error_records"`
	CopiedBytes         uint64 `json:"copied_bytes"`
	FirstFrame          uint64 `json:"first_frame"`
	LastFrame           uint64 `json:"last_frame"`
	CopiedPayloadSHA256 string `json:"copied_payload_sha256"`
	GuestPayloadSHA256  string `json:"guest_payload_sha256,omitempty"`
}

type Report struct {
	SchemaVersion            int           `json:"schema_version"`
	Scope                    string        `json:"scope"`
	InputAssumption          string        `json:"input_assumption"`
	PayloadDigestEncoding    string        `json:"payload_digest_encoding"`
	Profile                  string        `json:"profile"`
	MainSHA256               string        `json:"main_sha256"`
	SDKSHA256                string        `json:"sdk_sha256"`
	FileBytes                int64         `json:"file_bytes"`
	CapacityBytes            uint64        `json:"capacity_bytes"`
	CommittedBytes           uint64        `json:"committed_bytes"`
	ParsedRecords            uint64        `json:"parsed_records"`
	Header                   Counters      `json:"header"`
	Installed                bool          `json:"installed"`
	ProfileMatched           bool          `json:"profile_matched"`
	NormalClose              bool          `json:"capture_window_closed"`
	CommittedPrefixValid     bool          `json:"committed_prefix_valid"`
	CaptureIntegrityEligible bool          `json:"capture_integrity_eligible"`
	G0Qualified              bool          `json:"g0_qualified"`
	GameWorkerQualified      bool          `json:"game_worker_qualified"`
	Problems                 []string      `json:"problems"`
	Kinds                    []KindSummary `json:"kinds"`
}

type header struct {
	capacity, committed, flags, owner, observationCap uint64
	counts                                            Counters
}

type summary struct {
	value  KindSummary
	copied hash.Hash
	guest  hash.Hash
}

// Read requires a finished owning process or an externally verified stable
// snapshot after explicit capture-window closure. Eligibility covers only that
// declared interval; a valid committed prefix without closure is provisional.
func Read(input io.ReaderAt, size int64) (*Report, error) {
	if size < HeaderSize {
		return nil, fmt.Errorf("trace has %d bytes, requires header %d", size, HeaderSize)
	}
	b := make([]byte, HeaderSize)
	if err := readAt(input, b, 0); err != nil {
		return nil, err
	}
	h, err := decodeHeader(b, size)
	if err != nil {
		return nil, err
	}
	r := &Report{
		SchemaVersion:         2,
		Scope:                 "private fixed-profile Darwin ARM64 local observer; bounded declared capture window only; no network or game qualification",
		InputAssumption:       "owning process finished or explicitly closed capture window with externally verified stable snapshot",
		PayloadDigestEncoding: "SHA-256 of concatenated copied bytes per kind in sequence order; only server postpop guest digest excludes CID4; client postpop copied digest is payload-only; attempts are not delivery and client dequeue is not gameplay dispatch",
		Profile:               ProfileName,
		MainSHA256:            mainSHA256,
		SDKSHA256:             sdkSHA256,
		FileBytes:             size,
		CapacityBytes:         h.capacity,
		CommittedBytes:        h.committed,
		Header:                h.counts,
		Installed:             h.flags&installed != 0,
		ProfileMatched:        h.flags&profileMatched != 0,
		NormalClose:           h.flags&closed != 0,
		CommittedPrefixValid:  true,
		Problems:              []string{},
		Kinds:                 make([]KindSummary, len(kindNames)),
	}
	kinds := [len(kindNames)]summary{}
	for i, name := range kindNames {
		kinds[i] = summary{value: KindSummary{Kind: name}, copied: sha256.New(), guest: sha256.New()}
	}
	var attempts, pops, empty, errorRecords uint64
	for at := uint64(HeaderSize); at < h.committed; {
		if h.committed-at < RecordHeaderSize {
			return nil, fmt.Errorf("committed prefix ends inside record header at %d", at)
		}
		if err := readAt(input, b[:RecordHeaderSize], int64(at)); err != nil {
			return nil, err
		}
		rec, err := decodeRecord(b[:RecordHeaderSize], h, r.ParsedRecords+1, at)
		if err != nil {
			return nil, err
		}
		if uint64(rec.size) > h.committed-at {
			return nil, fmt.Errorf("record %d exceeds committed prefix", rec.sequence)
		}
		payload := make([]byte, rec.length)
		if err := readAt(input, payload, int64(at+RecordHeaderSize)); err != nil {
			return nil, err
		}
		padding := make([]byte, int(rec.size)-RecordHeaderSize-int(rec.length))
		if err := readAt(input, padding, int64(at+RecordHeaderSize+uint64(rec.length))); err != nil {
			return nil, err
		}
		if !zero(padding) {
			return nil, fmt.Errorf("record %d has nonzero padding", rec.sequence)
		}
		k := &kinds[rec.kind-1]
		if err := observe(k, rec, payload); err != nil {
			return nil, err
		}
		if rec.kind <= 2 {
			attempts++
		} else {
			pops++
		}
		if rec.flags&emptyFlag != 0 {
			empty++
		}
		if rec.flags&(badSpanFlag|capExceededFlag|threadViolationFlag|recordGapFlag) != 0 {
			errorRecords++
		}
		r.ParsedRecords++
		at += uint64(rec.size)
	}
	if err := validateCounts(h, r.ParsedRecords, attempts, pops, empty, errorRecords); err != nil {
		return nil, err
	}
	if !r.Installed {
		r.Problems = append(r.Problems, "observer_not_installed")
	}
	if !r.ProfileMatched {
		r.Problems = append(r.Problems, "native_profile_not_matched")
	}
	if !r.NormalClose {
		r.Problems = append(r.Problems, "provisional_prefix_without_window_close")
	}
	if uint64(size) != h.capacity {
		r.Problems = append(r.Problems, "file_capacity_truncated")
	}
	if h.counts.Records != r.ParsedRecords {
		r.Problems = append(r.Problems, "uncommitted_record_count")
	}
	if h.flags&captureGap != 0 {
		r.Problems = append(r.Problems, "native_capture_gap")
	}
	if h.counts.Errors != 0 {
		r.Problems = append(r.Problems, "native_observation_errors")
	}
	if h.counts.Overflow != 0 {
		r.Problems = append(r.Problems, "native_capacity_overflow")
	}
	if h.counts.ThreadViolations != 0 {
		r.Problems = append(r.Problems, "native_thread_violations")
	}
	if errorRecords != 0 {
		r.Problems = append(r.Problems, "record_observation_errors")
	}
	for i := range kinds {
		k := &kinds[i]
		k.value.CopiedPayloadSHA256 = hex.EncodeToString(k.copied.Sum(nil))
		if i >= 2 && i <= 3 {
			k.value.GuestPayloadSHA256 = hex.EncodeToString(k.guest.Sum(nil))
		}
		r.Kinds[i] = k.value
	}
	r.CaptureIntegrityEligible = len(r.Problems) == 0
	return r, nil
}

func decodeHeader(b []byte, size int64) (header, error) {
	u64 := func(at int) uint64 { return binary.LittleEndian.Uint64(b[at:]) }
	h := header{capacity: u64(16), committed: u64(24), flags: u64(88), owner: u64(96), observationCap: u64(104), counts: Counters{Records: u64(32), SendAttempts: u64(40), PopCalls: u64(48), EmptyPops: u64(56), Errors: u64(64), Overflow: u64(72), ThreadViolations: u64(80)}}
	if string(b[:8]) != "D2RTP002" {
		return h, fmt.Errorf("unsupported trace magic")
	}
	if binary.LittleEndian.Uint32(b[8:]) != 2 || binary.LittleEndian.Uint32(b[12:]) != HeaderSize {
		return h, fmt.Errorf("unsupported trace version or header size")
	}
	if h.capacity != Capacity {
		return h, fmt.Errorf("invalid trace capacity %d", h.capacity)
	}
	if uint64(size) > h.capacity || h.committed < HeaderSize || h.committed > h.capacity || h.committed > uint64(size) || h.committed%8 != 0 {
		return h, fmt.Errorf("invalid committed prefix or file extent")
	}
	if h.flags & ^uint64(installed|profileMatched|closed|captureGap) != 0 || !zero(b[176:]) {
		return h, fmt.Errorf("unknown header flags or nonzero reserved bytes")
	}
	if h.owner == 0 || h.observationCap != ObservationCap {
		return h, fmt.Errorf("invalid owner thread or observation cap")
	}
	if hex.EncodeToString(b[112:144]) != mainSHA256 || hex.EncodeToString(b[144:176]) != sdkSHA256 {
		return h, fmt.Errorf("trace module identity differs from fixed profile")
	}
	if h.counts.Records > (h.capacity-HeaderSize)/RecordHeaderSize {
		return h, fmt.Errorf("native record count exceeds capacity")
	}
	if h.counts.Records > h.counts.SendAttempts && h.counts.Records-h.counts.SendAttempts > h.counts.PopCalls {
		return h, fmt.Errorf("native records exceed method calls")
	}
	if h.counts.EmptyPops > h.counts.PopCalls || h.counts.ThreadViolations > h.counts.Errors || h.counts.Overflow > h.counts.Errors {
		return h, fmt.Errorf("inconsistent native counters")
	}
	if h.counts.Errors != 0 && h.flags&captureGap == 0 {
		return h, fmt.Errorf("native errors lack capture gap flag")
	}
	return h, nil
}

func readAt(r io.ReaderAt, b []byte, at int64) error {
	if len(b) == 0 {
		return nil
	}
	n, err := r.ReadAt(b, at)
	if err != nil {
		return fmt.Errorf("read trace at %d: %w", at, err)
	}
	if n != len(b) {
		return fmt.Errorf("read trace at %d: %w", at, io.ErrUnexpectedEOF)
	}
	return nil
}
func zero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
