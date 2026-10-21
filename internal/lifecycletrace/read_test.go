package lifecycletrace

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

type sample struct {
	gate, phase, flags, code uint32
	frame, transport         uint64
	stateBA1                 bool
}

func put64(b []byte, at int, v uint64) { binary.LittleEndian.PutUint64(b[at:], v) }
func put32(b []byte, at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
func fixture(records ...sample) []byte {
	b := make([]byte, HeaderSize+RecordSize*len(records))
	copy(b, "D2RLC001")
	put32(b, 8, 1)
	put32(b, 12, HeaderSize)
	put64(b, 16, Capacity)
	put64(b, 24, uint64(len(b)))
	put64(b, 32, uint64(len(records)))
	put64(b, 72, started|profileMatched|closed)
	put64(b, 80, 42)
	put64(b, 88, RecordSize)
	put64(b, 96, 100)
	put64(b, 104, 100000)
	m, _ := hex.DecodeString(mainSHA256)
	s, _ := hex.DecodeString(sdkSHA256)
	copy(b[112:], m)
	copy(b[144:], s)
	var callbacks, errs uint64
	for i, r := range records {
		p := b[HeaderSize+i*RecordSize:]
		put64(p, 0, uint64(i+1))
		put64(p, 8, r.frame)
		put64(p, 16, uint64(1000+i))
		put64(p, 24, r.transport)
		put32(p, 32, r.gate)
		put32(p, 36, r.phase)
		put32(p, 56, r.flags)
		put32(p, 84, r.code)
		if r.flags&uiMetadata != 0 {
			put64(p, 40, 0x1122334455667788)
			put64(p, 48, 0x8877665544332211)
		}
		if r.phase == 0 {
			callbacks++
		}
		if r.flags&uiUnreadable != 0 {
			errs++
		}
		if r.flags&parentUnreadable != 0 {
			errs++
		}
		if r.stateBA1 {
			p[136] = 1
		}
	}
	put64(b, 40, callbacks)
	put64(b, 48, errs)
	if errs != 0 {
		put64(b, 72, started|profileMatched|closed|gap)
	}
	return b
}
func pair(gate uint32) []sample {
	flags := uint32(0)
	if gate == 1 {
		flags = uiMetadata
	}
	return []sample{{gate: gate, flags: flags}, {gate: gate, phase: 1, flags: flags}}
}
func readFixture(t *testing.T, b []byte) *Report {
	t.Helper()
	r, err := Read(bytes.NewReader(b), Capacity)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNestedCallbacksAndChangingState(t *testing.T) {
	b := fixture(sample{gate: 1, flags: uiMetadata | parentPresent | parentState}, sample{gate: 2, frame: 1, transport: 4},
		sample{gate: 2, phase: 1, frame: 1, transport: 5, code: 30, stateBA1: true}, sample{gate: 1, phase: 1, flags: uiMetadata, frame: 2, transport: 5, code: 8, stateBA1: true})
	p := b[HeaderSize+2*RecordSize:]
	put32(p, 60, 5)
	put32(p, 64, 22)
	put32(p, 68, 33)
	put32(p, 72, 44)
	put32(p, 100, 3)
	put32(p, 88, math.MaxUint32)
	put32(p, 92, 1)
	put64(p, 128, math.MaxUint64)
	put64(b, HeaderSize+3*RecordSize+40, 123)
	r := readFixture(t, b)
	if !r.CaptureIntegrityEligible || !r.CommittedPrefixValid || r.CauseEstablished || r.G0Qualified || r.GameWorkerQualified || r.ParsedRecords != 4 || r.MaximumCallbackDepth != 2 || r.OpenCallbacks != 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
	if r.Gates[0].Entries != 1 || r.Gates[0].Returns != 1 || r.Gates[1].Entries != 1 || r.Gates[1].Returns != 1 || r.Header.Callbacks != 2 {
		t.Fatalf("wrong callback summaries: %+v", r)
	}
	f := r.FirstPendingError
	if !f.Observed || f.Code != 30 || f.Frame != 1 || f.Gate != 2 || f.Phase != 1 || f.OperationMode != 5 || f.TransportMode != 3 || f.StateB94 != 33 || f.ErrorDeadlineRaw != math.MaxUint64 || f.Timer1F0Raw != math.MaxUint32 || f.Timer1F4Raw != 1 {
		t.Fatalf("raw units or pending context changed: %+v", f)
	}
	if len(r.ErrorCodes) != 3 || r.ErrorCodes[0].Code != 0 || r.ErrorCodes[0].Snapshots != 2 || r.ErrorCodes[1].Code != 8 || r.ErrorCodes[2].Code != 30 {
		t.Fatalf("wrong raw code counts: %+v", r.ErrorCodes)
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ui_category", "ui_action", "owner_thread", "last_receive", "last_send", "payload", "pointer", "timeline"} {
		if bytes.Contains(encoded, []byte("\""+forbidden+"\"")) {
			t.Fatalf("report exposes %s", forbidden)
		}
	}
}

func TestEmptyCaptureAndUnconstrainedErrorCodes(t *testing.T) {
	r := readFixture(t, fixture())
	if !r.CaptureIntegrityEligible || r.FirstPendingError.Observed || len(r.ErrorCodes) != 0 || len(r.Gates) != 2 {
		t.Fatalf("empty capture: %+v", r)
	}
	b := fixture(sample{gate: 2, code: math.MaxUint32}, sample{gate: 2, phase: 1, code: math.MaxUint32})
	r = readFixture(t, b)
	if !r.CaptureIntegrityEligible || r.ErrorCodes[0].Code != math.MaxUint32 || r.FirstPendingError.Observed {
		t.Fatalf("raw code was constrained or treated as a pending error: %+v", r)
	}
}

func TestPendingErrorUsesDeadlineRatherThanSharedLifecycleState(t *testing.T) {
	b := fixture(sample{gate: 2, code: 30, stateBA1: true},
		sample{gate: 2, phase: 1, code: 30, stateBA1: true})
	r := readFixture(t, b)
	if r.FirstPendingError.Observed || r.ErrorCodes[0].Code != 30 || r.ErrorCodes[0].Snapshots != 2 {
		t.Fatalf("shared lifecycle state or stale code became a pending error: %+v", r)
	}
	b = fixture(sample{gate: 2, code: 8}, sample{gate: 2, phase: 1, code: 30})
	put64(b, HeaderSize+128, 12345)
	put64(b, HeaderSize+RecordSize+128, 23456)
	r = readFixture(t, b)
	if !r.FirstPendingError.Observed || r.FirstPendingError.Code != 8 || r.FirstPendingError.Phase != 0 || r.FirstPendingError.ErrorDeadlineRaw != 12345 {
		t.Fatalf("pending deadline before state-byte update was missed: %+v", r.FirstPendingError)
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"first_pending_error"`)) || bytes.Contains(encoded, []byte(`"first_latched_error"`)) {
		t.Fatalf("incorrect report field: %s", encoded)
	}
}

func TestHeaderCorruption(t *testing.T) {
	cases := map[string]func([]byte){
		"magic": func(b []byte) { b[0] = 'X' }, "version": func(b []byte) { put32(b, 8, 2) }, "header size": func(b []byte) { put32(b, 12, 255) },
		"capacity": func(b []byte) { put64(b, 16, Capacity+1) }, "record size": func(b []byte) { put64(b, 88, 159) },
		"prefix before header": func(b []byte) { put64(b, 24, 0) }, "unaligned prefix": func(b []byte) { put64(b, 24, 257) },
		"prefix beyond capacity": func(b []byte) { put64(b, 24, Capacity+RecordSize) }, "owner": func(b []byte) { put64(b, 80, 0) },
		"unknown flags": func(b []byte) { put64(b, 72, 16) }, "reserved": func(b []byte) { b[255] = 1 },
		"main": func(b []byte) { b[112] ^= 1 }, "sdk": func(b []byte) { b[144] ^= 1 },
		"start time": func(b []byte) { put64(b, 96, 0) }, "closed before start": func(b []byte) { put64(b, 104, 99) }, "closed without time": func(b []byte) { put64(b, 104, 0) },
		"records cap": func(b []byte) { put64(b, 32, MaxRecords+1) }, "foreign count": func(b []byte) { put64(b, 64, 2) },
		"errors without gap": func(b []byte) { put64(b, 48, 1) }, "counter underflow": func(b []byte) { put64(b, 32, 0) },
		"closed uncommitted": func(b []byte) { put64(b, 32, 3) }, "closed missing callbacks": func(b []byte) { put64(b, 40, 2) },
		"overflow without errors": func(b []byte) { put64(b, 56, 1) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := fixture(pair(1)...)
			change(b)
			if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
				t.Fatal("accepted corruption")
			}
		})
	}
}

func TestRecordCorruption(t *testing.T) {
	cases := map[string]func([]byte){
		"sequence": func(p []byte) { put64(p, 0, 2) }, "gate": func(p []byte) { put32(p, 32, 3) }, "phase": func(p []byte) { put32(p, 36, 2) },
		"reserved0": func(p []byte) { put32(p, 124, 1) }, "reserved tail": func(p []byte) { p[159] = 1 }, "unknown flags": func(p []byte) { put32(p, 56, 32) },
		"missing UI outcome": func(p []byte) { put32(p, 56, 0) }, "both UI outcomes": func(p []byte) { put32(p, 56, 3) },
		"tick UI flags": func(p []byte) { put32(p, 32, 2) }, "tick hashes": func(p []byte) { put32(p, 32, 2); put32(p, 56, 0) },
		"unreadable UI hashes":    func(p []byte) { put32(p, 56, uiUnreadable) },
		"parent state only":       func(p []byte) { put32(p, 56, uiMetadata|parentState) },
		"parent present alone":    func(p []byte) { put32(p, 56, uiMetadata|parentPresent) },
		"both parent outcomes":    func(p []byte) { put32(p, 56, uiMetadata|parentPresent|parentState|parentUnreadable) },
		"unflagged parent state":  func(p []byte) { put32(p, 120, 1) },
		"unreadable parent state": func(p []byte) { put32(p, 56, uiMetadata|parentPresent|parentUnreadable); put32(p, 120, 1) },
		"time before start":       func(p []byte) { put64(p, 16, 99) }, "time after close": func(p []byte) { put64(p, 16, 100001) },
		"return without entry": func(p []byte) { put32(p, 36, 1) },
	}
	for i := 136; i < 142; i++ {
		offset := i
		cases["boolean "+string(rune('a'+i-136))] = func(p []byte) { p[offset] = 2 }
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := fixture(pair(1)...)
			change(b[HeaderSize:])
			if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
				t.Fatal("accepted corruption")
			}
		})
	}
	for _, offset := range []int{8, 16, 24} {
		t.Run("decreasing counter", func(t *testing.T) {
			b := fixture(sample{gate: 2, frame: 5, transport: 7}, sample{gate: 2, phase: 1, frame: 5, transport: 7})
			put64(b, HeaderSize+RecordSize+offset, 1)
			if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
				t.Fatal("accepted decreasing counter")
			}
		})
	}
	b := fixture(sample{gate: 1, flags: uiMetadata}, sample{gate: 2}, sample{gate: 1, phase: 1, flags: uiMetadata}, sample{gate: 2, phase: 1})
	if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
		t.Fatal("accepted crossed nesting")
	}
}

func TestUnreadableAndNativeFailuresAreIneligible(t *testing.T) {
	for _, flags := range []uint32{uiUnreadable, uiMetadata | parentPresent | parentUnreadable, uiUnreadable | parentPresent | parentUnreadable} {
		b := fixture(sample{gate: 1, flags: flags}, sample{gate: 1, phase: 1, flags: flags})
		r := readFixture(t, b)
		if r.CaptureIntegrityEligible || r.Gates[0].UnreadableSnapshots != 2 {
			t.Fatalf("unreadable capture eligible: %+v", r)
		}
		put64(b, 48, 0)
		if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
			t.Fatal("unreadable fields without native errors accepted")
		}
	}
	cases := map[string]func([]byte){
		"not started": func(b []byte) { put64(b, 72, profileMatched|closed) }, "not matched": func(b []byte) { put64(b, 72, started|closed) },
		"gap only":                 func(b []byte) { put64(b, 72, started|profileMatched|closed|gap) },
		"close or reentrant error": func(b []byte) { put64(b, 48, 1); put64(b, 72, started|profileMatched|closed|gap) },
		"foreign callback": func(b []byte) {
			put64(b, 40, 1)
			put64(b, 48, 1)
			put64(b, 64, 1)
			put64(b, 72, started|profileMatched|closed|gap)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := fixture()
			change(b)
			r := readFixture(t, b)
			if r.CaptureIntegrityEligible || len(r.Problems) == 0 {
				t.Fatal("native failure eligible")
			}
		})
	}
}

func TestUnclosedAndTruncatedPrefixes(t *testing.T) {
	b := fixture(sample{gate: 1, flags: uiMetadata})
	put64(b, 72, started|profileMatched)
	put64(b, 104, 0)
	r := readFixture(t, b)
	if r.CaptureIntegrityEligible || r.OpenCallbacks != 1 || !r.CommittedPrefixValid {
		t.Fatalf("unclosed prefix: %+v", r)
	}
	put64(b, 40, 12)
	if r = readFixture(t, b); r.CaptureIntegrityEligible {
		t.Fatal("unfinished admitted callbacks eligible")
	}
	put64(b, 32, 2)
	r = readFixture(t, b)
	if r.CaptureIntegrityEligible {
		t.Fatal("uncommitted record eligible")
	}
	put64(b, 32, 3)
	if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
		t.Fatal("counter lead too large")
	}
	b = fixture(pair(2)...)
	r, err := Read(bytes.NewReader(b), int64(len(b)))
	if err != nil || r.CaptureIntegrityEligible {
		t.Fatalf("truncated tail: %+v %v", r, err)
	}
	if _, err = Read(bytes.NewReader(b), int64(len(b)-1)); err == nil {
		t.Fatal("accepted committed extent outside file")
	}
	if _, err = Read(bytes.NewReader(nil), 255); err == nil {
		t.Fatal("accepted missing header")
	}
	if _, err = Read(bytes.NewReader(b), Capacity+1); err == nil {
		t.Fatal("accepted oversized file")
	}
	b = fixture(sample{gate: 2})
	if _, err = Read(bytes.NewReader(b), Capacity); err == nil {
		t.Fatal("closed unfinished callback accepted")
	}
}

func TestOverflowAndBoundedSummaries(t *testing.T) {
	samples := make([]sample, MaxRecords)
	for i := range samples {
		samples[i] = sample{gate: 2, phase: uint32(i % 2), code: uint32(i)}
	}
	b := fixture(samples...)
	put64(b, 40, MaxRecords/2+1)
	put64(b, 48, 2)
	put64(b, 56, 2)
	put64(b, 72, started|profileMatched|closed|gap)
	r := readFixture(t, b)
	if r.CaptureIntegrityEligible || len(r.ErrorCodes) != MaxErrorCodes || r.OmittedErrorCodeSnapshots != MaxRecords-MaxErrorCodes || r.ParsedRecords != MaxRecords {
		t.Fatalf("unbounded or eligible overflow: %+v", r)
	}
	put64(b, 40, math.MaxUint64)
	if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
		t.Fatal("accepted overflowing count arithmetic")
	}
	b = fixture(pair(2)...)
	put64(b, 48, 1)
	put64(b, 56, 1)
	put64(b, 72, started|profileMatched|closed|gap)
	if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
		t.Fatal("overflow before full capacity accepted")
	}
	for i := range samples {
		samples[i] = sample{gate: 2}
	}
	b = fixture(samples...)
	put64(b, 48, MaxRecords)
	put64(b, 56, MaxRecords)
	put64(b, 72, started|profileMatched|closed|gap)
	r = readFixture(t, b)
	if r.CaptureIntegrityEligible || r.OpenCallbacks != MaxRecords || r.MaximumCallbackDepth != MaxRecords {
		t.Fatalf("overflow-truncated nested prefix: %+v", r)
	}
}

type failingReader struct {
	at    int64
	err   error
	short bool
	data  []byte
}

func (f failingReader) ReadAt(b []byte, at int64) (int, error) {
	if at == f.at {
		if f.short {
			return len(b) - 1, nil
		}
		return 0, f.err
	}
	return bytes.NewReader(f.data).ReadAt(b, at)
}
func TestIOErrorsRemainVisible(t *testing.T) {
	problem := errors.New("synthetic IO failure")
	b := fixture(pair(2)...)
	for _, at := range []int64{0, HeaderSize} {
		_, err := Read(failingReader{at: at, err: problem, data: b}, Capacity)
		if !errors.Is(err, problem) {
			t.Fatalf("lost IO chain: %v", err)
		}
	}
	_, err := Read(failingReader{at: HeaderSize, short: true, data: b}, Capacity)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short read: %v", err)
	}
	_, err = Read(bytes.NewReader(b[:HeaderSize]), Capacity)
	if !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "256") {
		t.Fatalf("missing record: %v", err)
	}
}
