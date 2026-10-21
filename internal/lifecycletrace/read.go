// Package lifecycletrace reads stable, bounded snapshots of host observation
// callbacks. Capture integrity does not establish a disconnect cause or gameplay.
package lifecycletrace

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
)

const (
	HeaderSize       = 256
	RecordSize       = 160
	MaxRecords       = 16384
	Capacity         = HeaderSize + RecordSize*MaxRecords
	MaxErrorCodes    = 32
	ProfileName      = "d2r-mobile-120fe55-darwin-arm64-lifecycle-v1"
	mainSHA256       = "30384eabe394cbc6b6a0b7f3788983d58dc14a92b7abe1638853dc4833cf81f1"
	sdkSHA256        = "d48f2cc33f3e506d623f873ba447c88fe0aea989b98c4057e2481b6ca3988fa2"
	started          = 1
	profileMatched   = 2
	closed           = 4
	gap              = 8
	uiMetadata       = 1
	uiUnreadable     = 2
	parentPresent    = 4
	parentState      = 8
	parentUnreadable = 16
)

type Counters struct {
	Records          uint64 `json:"records"`
	Callbacks        uint64 `json:"callbacks"`
	Errors           uint64 `json:"errors"`
	Overflow         uint64 `json:"overflow"`
	ThreadViolations uint64 `json:"thread_violations"`
}

type GateSummary struct {
	Gate                string `json:"gate"`
	Records             uint64 `json:"records"`
	Entries             uint64 `json:"callback_entries"`
	Returns             uint64 `json:"callback_returns"`
	UnreadableSnapshots uint64 `json:"unreadable_snapshots"`
}

type ErrorCodeSummary struct {
	Code      uint32 `json:"code"`
	Snapshots uint64 `json:"snapshots"`
}

// PendingError records the first nonzero error deadline, not a decoded cause. The timer
// values retain their native units and are never compared with host nanoseconds.
type PendingError struct {
	Observed         bool   `json:"observed"`
	Code             uint32 `json:"raw_error_code"`
	Frame            uint64 `json:"frame"`
	Gate             uint32 `json:"gate"`
	Phase            uint32 `json:"phase"`
	OperationMode    uint32 `json:"operation_mode"`
	TransportMode    uint32 `json:"transport_mode"`
	StateB90         uint32 `json:"state_b90"`
	StateB94         uint32 `json:"state_b94"`
	StateB98         uint32 `json:"state_b98"`
	ErrorDeadlineRaw uint64 `json:"error_deadline_raw"`
	Timer1F0Raw      uint32 `json:"timer_1f0_raw"`
	Timer1F4Raw      uint32 `json:"timer_1f4_raw"`
}

type Report struct {
	SchemaVersion             int                `json:"schema_version"`
	Scope                     string             `json:"scope"`
	InputAssumption           string             `json:"input_assumption"`
	Profile                   string             `json:"profile"`
	MainSHA256                string             `json:"main_sha256"`
	SDKSHA256                 string             `json:"sdk_sha256"`
	FileBytes                 int64              `json:"file_bytes"`
	CapacityBytes             uint64             `json:"capacity_bytes"`
	CommittedBytes            uint64             `json:"committed_bytes"`
	ParsedRecords             uint64             `json:"parsed_records"`
	Header                    Counters           `json:"header"`
	Started                   bool               `json:"started"`
	ProfileMatched            bool               `json:"profile_matched"`
	NormalClose               bool               `json:"capture_window_closed"`
	CommittedPrefixValid      bool               `json:"committed_prefix_valid"`
	CaptureIntegrityEligible  bool               `json:"capture_integrity_eligible"`
	CauseEstablished          bool               `json:"disconnect_cause_established"`
	G0Qualified               bool               `json:"g0_qualified"`
	GameWorkerQualified       bool               `json:"game_worker_qualified"`
	MaximumCallbackDepth      uint64             `json:"maximum_callback_depth"`
	OpenCallbacks             uint64             `json:"open_callbacks_in_prefix"`
	Gates                     []GateSummary      `json:"gates"`
	ErrorCodes                []ErrorCodeSummary `json:"raw_error_codes"`
	OmittedErrorCodeSnapshots uint64             `json:"omitted_error_code_snapshots"`
	FirstPendingError         PendingError       `json:"first_pending_error"`
	Problems                  []string           `json:"problems"`
}

type header struct {
	committed, flags, startedNS, closedNS uint64
	counts                                Counters
}

// Read requires an ended owning process or an externally verified stable
// snapshot after capture closure. It reads only the committed prefix, using one
// fixed record buffer and a stack bounded by the native record capacity.
func Read(input io.ReaderAt, size int64) (*Report, error) {
	if size < HeaderSize {
		return nil, fmt.Errorf("lifecycle trace has %d bytes, requires header %d", size, HeaderSize)
	}
	var b [HeaderSize]byte
	if err := readAt(input, b[:], 0); err != nil {
		return nil, err
	}
	h, err := decodeHeader(b[:], size)
	if err != nil {
		return nil, err
	}
	r := &Report{
		SchemaVersion:   1,
		Scope:           "fixed Darwin ARM64 host observation callbacks before/after upstream callback; bounded capture interval only; no Guest-body or disconnect-cause qualification",
		InputAssumption: "owning process ended or capture explicitly closed with externally verified stable snapshot",
		Profile:         ProfileName, MainSHA256: mainSHA256, SDKSHA256: sdkSHA256,
		FileBytes: size, CapacityBytes: Capacity, CommittedBytes: h.committed, Header: h.counts,
		Started: h.flags&started != 0, ProfileMatched: h.flags&profileMatched != 0,
		NormalClose: h.flags&closed != 0, CommittedPrefixValid: true,
		Gates: []GateSummary{{Gate: "ui"}, {Gate: "tick"}}, ErrorCodes: []ErrorCodeSummary{}, Problems: []string{},
	}
	stack := make([]uint32, 0)
	codes := make(map[uint32]uint64)
	var entries, returns, unreadable, previousTime, previousFrame, previousTransport uint64
	for at := uint64(HeaderSize); at < h.committed; at += RecordSize {
		if err := readAt(input, b[:RecordSize], int64(at)); err != nil {
			return nil, err
		}
		x, err := decodeRecord(b[:RecordSize], r.ParsedRecords+1)
		if err != nil {
			return nil, err
		}
		if x.monotonicNS < h.startedNS || (h.closedNS != 0 && x.monotonicNS > h.closedNS) ||
			(r.ParsedRecords != 0 && (x.monotonicNS < previousTime || x.frame < previousFrame || x.transportSequence < previousTransport)) {
			return nil, fmt.Errorf("record %d has inconsistent host time, frame or transport sequence", x.sequence)
		}
		previousTime, previousFrame, previousTransport = x.monotonicNS, x.frame, x.transportSequence
		g := &r.Gates[x.gate-1]
		g.Records++
		if x.phase == 0 {
			stack = append(stack, x.gate)
			entries++
			g.Entries++
			if uint64(len(stack)) > r.MaximumCallbackDepth {
				r.MaximumCallbackDepth = uint64(len(stack))
			}
		} else {
			if len(stack) == 0 || stack[len(stack)-1] != x.gate {
				return nil, fmt.Errorf("record %d does not return from the active callback", x.sequence)
			}
			stack = stack[:len(stack)-1]
			returns++
			g.Returns++
		}
		if x.flags&(uiUnreadable|parentUnreadable) != 0 {
			g.UnreadableSnapshots++
		}
		if x.flags&uiUnreadable != 0 {
			unreadable++
		}
		if x.flags&parentUnreadable != 0 {
			unreadable++
		}
		if count, exists := codes[x.errorCode]; exists || len(codes) < MaxErrorCodes {
			codes[x.errorCode] = count + 1
		} else {
			r.OmittedErrorCodeSnapshots++
		}
		if !r.FirstPendingError.Observed && x.errorDeadline != 0 {
			r.FirstPendingError = PendingError{Observed: true, Code: x.errorCode, Frame: x.frame, Gate: x.gate, Phase: x.phase,
				OperationMode: x.operationMode, TransportMode: x.transportMode, StateB90: x.stateB90, StateB94: x.stateB94, StateB98: x.stateB98,
				ErrorDeadlineRaw: x.errorDeadline, Timer1F0Raw: x.timer1F0, Timer1F4Raw: x.timer1F4}
		}
		r.ParsedRecords++
	}
	r.OpenCallbacks = uint64(len(stack))
	if err := validateCounts(h, r.ParsedRecords, entries, returns, unreadable); err != nil {
		return nil, err
	}
	for code, count := range codes {
		r.ErrorCodes = append(r.ErrorCodes, ErrorCodeSummary{Code: code, Snapshots: count})
	}
	sort.Slice(r.ErrorCodes, func(i, j int) bool { return r.ErrorCodes[i].Code < r.ErrorCodes[j].Code })
	for _, issue := range []struct {
		present bool
		name    string
	}{
		{!r.Started, "observer_not_started"}, {!r.ProfileMatched, "native_profile_not_matched"},
		{!r.NormalClose, "provisional_prefix_without_window_close"}, {size != Capacity, "file_capacity_truncated"},
		{h.counts.Records != r.ParsedRecords, "uncommitted_record_count"}, {h.flags&gap != 0, "native_capture_gap"},
		{h.counts.Errors != 0, "native_observation_errors"}, {h.counts.Overflow != 0, "native_capacity_overflow"},
		{h.counts.ThreadViolations != 0, "native_thread_violations"}, {unreadable != 0, "unreadable_snapshot_fields"},
		{r.OpenCallbacks != 0, "unfinished_callback_prefix"},
	} {
		if issue.present {
			r.Problems = append(r.Problems, issue.name)
		}
	}
	r.CaptureIntegrityEligible = len(r.Problems) == 0
	return r, nil
}

func decodeHeader(b []byte, size int64) (header, error) {
	u64 := func(at int) uint64 { return binary.LittleEndian.Uint64(b[at:]) }
	h := header{committed: u64(24), flags: u64(72), startedNS: u64(96), closedNS: u64(104),
		counts: Counters{Records: u64(32), Callbacks: u64(40), Errors: u64(48), Overflow: u64(56), ThreadViolations: u64(64)}}
	if string(b[:8]) != "D2RLC001" || binary.LittleEndian.Uint32(b[8:]) != 1 || binary.LittleEndian.Uint32(b[12:]) != HeaderSize {
		return h, fmt.Errorf("unsupported lifecycle trace format")
	}
	if u64(16) != Capacity || u64(88) != RecordSize || uint64(size) > Capacity || h.committed < HeaderSize || h.committed > uint64(size) || (h.committed-HeaderSize)%RecordSize != 0 {
		return h, fmt.Errorf("invalid lifecycle capacity, record size or committed extent")
	}
	if h.flags & ^uint64(started|profileMatched|closed|gap) != 0 || !zero(b[176:]) || u64(80) == 0 {
		return h, fmt.Errorf("invalid lifecycle flags, owner or reserved bytes")
	}
	if h.startedNS == 0 || (h.closedNS != 0 && h.closedNS < h.startedNS) || (h.flags&closed != 0 && h.closedNS == 0) {
		return h, fmt.Errorf("invalid lifecycle capture timestamps")
	}
	if hex.EncodeToString(b[112:144]) != mainSHA256 || hex.EncodeToString(b[144:176]) != sdkSHA256 {
		return h, fmt.Errorf("lifecycle modules differ from fixed profile")
	}
	if h.counts.Records > MaxRecords || h.counts.ThreadViolations > h.counts.Callbacks || h.counts.Errors != 0 && h.flags&gap == 0 {
		return h, fmt.Errorf("inconsistent lifecycle counters or error flag")
	}
	return h, nil
}

func validateCounts(h header, records, entries, returns, unreadable uint64) error {
	if h.counts.Records < records || h.counts.Records-records > 1 || h.counts.Callbacks < h.counts.ThreadViolations || h.counts.Callbacks-h.counts.ThreadViolations < entries {
		return fmt.Errorf("native counters contradict committed callbacks")
	}
	remaining := h.counts.Errors
	for _, required := range []uint64{unreadable, h.counts.Overflow, h.counts.ThreadViolations} {
		if required > remaining {
			return fmt.Errorf("native errors do not account for unreadable, overflow or foreign-thread outcomes")
		}
		remaining -= required
	}
	if h.counts.Overflow != 0 && records != MaxRecords {
		return fmt.Errorf("overflow without a full record prefix")
	}
	if h.flags&closed != 0 {
		if h.counts.Records != records {
			return fmt.Errorf("closed record count differs from committed prefix")
		}
		ownerCallbacks := h.counts.Callbacks - h.counts.ThreadViolations
		if ownerCallbacks > (^uint64(0))/2 || records > ^uint64(0)-h.counts.Overflow || records+h.counts.Overflow != 2*ownerCallbacks {
			return fmt.Errorf("closed callback counts do not account for both snapshots")
		}
		if h.counts.Overflow == 0 && entries != returns {
			return fmt.Errorf("closed capture has an unfinished callback")
		}
	}
	// Admission precedes the owner check and snapshot. An interrupted process can
	// leave admitted callbacks without records or their foreign-thread error yet.
	return nil
}

func readAt(r io.ReaderAt, b []byte, at int64) error {
	n, err := r.ReadAt(b, at)
	if err != nil {
		return fmt.Errorf("read lifecycle trace at %d: %w", at, err)
	}
	if n != len(b) {
		return fmt.Errorf("read lifecycle trace at %d: %w", at, io.ErrUnexpectedEOF)
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
