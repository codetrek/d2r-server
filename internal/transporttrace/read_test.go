package transporttrace

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

type fixtureRecord struct {
	kind, flags uint16
	frame       uint64
	cid, mode   uint32
	payload     []byte
	original    uint64
}

func fixture(records ...fixtureRecord) []byte {
	b := make([]byte, HeaderSize)
	copy(b, "D2RTP001")
	binary.LittleEndian.PutUint32(b[8:], 1)
	binary.LittleEndian.PutUint32(b[12:], HeaderSize)
	binary.LittleEndian.PutUint64(b[16:], Capacity)
	binary.LittleEndian.PutUint64(b[88:], installed|profileMatched|closed)
	binary.LittleEndian.PutUint64(b[96:], 42)
	binary.LittleEndian.PutUint64(b[104:], ObservationCap)
	main, _ := hex.DecodeString(mainSHA256)
	sdk, _ := hex.DecodeString(sdkSHA256)
	copy(b[112:], main)
	copy(b[144:], sdk)
	var attempts, pops, empty, errs uint64
	for i, r := range records {
		n := (RecordHeaderSize + len(r.payload) + 7) &^ 7
		p := make([]byte, n)
		binary.LittleEndian.PutUint32(p, uint32(n))
		binary.LittleEndian.PutUint16(p[4:], r.kind)
		binary.LittleEndian.PutUint16(p[6:], r.flags)
		binary.LittleEndian.PutUint64(p[8:], uint64(i+1))
		binary.LittleEndian.PutUint64(p[16:], r.frame)
		binary.LittleEndian.PutUint64(p[24:], 42)
		binary.LittleEndian.PutUint32(p[32:], r.cid)
		binary.LittleEndian.PutUint32(p[36:], r.mode)
		binary.LittleEndian.PutUint32(p[40:], uint32(len(r.payload)))
		binary.LittleEndian.PutUint64(p[48:], r.original)
		copy(p[64:], r.payload)
		b = append(b, p...)
		if r.kind <= 2 {
			attempts++
		} else {
			pops++
		}
		if r.flags&emptyFlag != 0 {
			empty++
		}
		if r.flags&(badSpanFlag|capExceededFlag|threadViolationFlag|recordGapFlag) != 0 {
			errs++
		}
	}
	binary.LittleEndian.PutUint64(b[24:], uint64(len(b)))
	binary.LittleEndian.PutUint64(b[32:], uint64(len(records)))
	binary.LittleEndian.PutUint64(b[40:], attempts)
	binary.LittleEndian.PutUint64(b[48:], pops)
	binary.LittleEndian.PutUint64(b[56:], empty)
	binary.LittleEndian.PutUint64(b[64:], errs)
	if errs != 0 {
		binary.LittleEndian.PutUint64(b[88:], installed|profileMatched|closed|captureGap)
	}
	return b
}

func golden() []byte {
	return fixture(
		fixtureRecord{kind: 1, flags: payloadFlag | clientIDFlag, mode: 1, payload: []byte{0xaa, 0xbb}, original: 2},
		fixtureRecord{kind: 2, flags: ignoredFlag},
		fixtureRecord{kind: 3, flags: payloadFlag | clientIDFlag, cid: 0x78563412, frame: 1, payload: []byte{0x12, 0x34, 0x56, 0x78, 0x16, 0x80}, original: 6},
		fixtureRecord{kind: 4, flags: emptyFlag, frame: 1},
		fixtureRecord{kind: 4, flags: payloadFlag | clientIDFlag, cid: 3, frame: 1, payload: []byte{3, 0, 0, 0, 0xff}, original: 5},
	)
}

func TestGoldenSummary(t *testing.T) {
	r, err := Read(bytes.NewReader(golden()), Capacity)
	if err != nil {
		t.Fatal(err)
	}
	if !r.CaptureIntegrityEligible || !r.CommittedPrefixValid || r.G0Qualified || r.GameWorkerQualified || r.ParsedRecords != 5 || len(r.Kinds) != 4 {
		t.Fatalf("unexpected report %+v", r)
	}
	if r.Kinds[0].PayloadRecords != 1 || r.Kinds[1].Ignored != 1 || r.Kinds[2].CopiedBytes != 6 || r.Kinds[3].Empty != 1 || r.Kinds[3].PayloadRecords != 1 {
		t.Fatalf("unexpected summaries %+v", r.Kinds)
	}
	h := sha256.Sum256([]byte{0x16, 0x80})
	if r.Kinds[2].GuestPayloadSHA256 != hex.EncodeToString(h[:]) {
		t.Fatalf("wrong guest payload digest: %s", r.Kinds[2].GuestPayloadSHA256)
	}
	if r.Kinds[0].GuestPayloadSHA256 != "" || r.Kinds[2].FirstFrame != 1 || r.Kinds[2].LastFrame != 1 {
		t.Fatal("attempt or frame scope changed")
	}
}

func TestHeaderCorruption(t *testing.T) {
	cases := map[string]func([]byte){
		"magic":                     func(b []byte) { b[0] = 'X' },
		"version":                   func(b []byte) { binary.LittleEndian.PutUint32(b[8:], 2) },
		"header size":               func(b []byte) { binary.LittleEndian.PutUint32(b[12:], 248) },
		"capacity":                  func(b []byte) { binary.LittleEndian.PutUint64(b[16:], Capacity-8) },
		"committed before header":   func(b []byte) { binary.LittleEndian.PutUint64(b[24:], 248) },
		"committed beyond capacity": func(b []byte) { binary.LittleEndian.PutUint64(b[24:], Capacity+8) },
		"unaligned committed":       func(b []byte) { binary.LittleEndian.PutUint64(b[24:], 257) },
		"unknown flags":             func(b []byte) { binary.LittleEndian.PutUint64(b[88:], 16) },
		"reserved":                  func(b []byte) { b[255] = 1 },
		"owner":                     func(b []byte) { binary.LittleEndian.PutUint64(b[96:], 0) },
		"cap":                       func(b []byte) { binary.LittleEndian.PutUint64(b[104:], ObservationCap+1) },
		"main identity":             func(b []byte) { b[112] ^= 1 },
		"sdk identity":              func(b []byte) { b[144] ^= 1 },
		"empty count":               func(b []byte) { binary.LittleEndian.PutUint64(b[56:], 100) },
		"native errors without gap": func(b []byte) { binary.LittleEndian.PutUint64(b[64:], 1) },
		"overflow without errors":   func(b []byte) { binary.LittleEndian.PutUint64(b[72:], 1) },
		"closed record count":       func(b []byte) { binary.LittleEndian.PutUint64(b[32:], 6) },
		"closed attempt count":      func(b []byte) { binary.LittleEndian.PutUint64(b[40:], 3) },
		"too few pop calls":         func(b []byte) { binary.LittleEndian.PutUint64(b[48:], 2) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := golden()
			mutate(b)
			if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
				t.Fatal("accepted corrupt header")
			}
		})
	}
	for _, size := range []int64{0, 255, Capacity + 1} {
		if _, err := Read(bytes.NewReader(golden()), size); err == nil {
			t.Fatalf("accepted extent %d", size)
		}
	}
}

func TestRecordCorruption(t *testing.T) {
	cases := map[string]func([]byte){
		"size":                 func(b []byte) { binary.LittleEndian.PutUint32(b[256:], 64) },
		"kind":                 func(b []byte) { binary.LittleEndian.PutUint16(b[260:], 5) },
		"unknown flag":         func(b []byte) { binary.LittleEndian.PutUint16(b[262:], 256) },
		"sequence":             func(b []byte) { binary.LittleEndian.PutUint64(b[264:], 2) },
		"thread":               func(b []byte) { binary.LittleEndian.PutUint64(b[280:], 43) },
		"reserved0":            func(b []byte) { b[300] = 1 },
		"reserved1":            func(b []byte) { b[312] = 1 },
		"padding":              func(b []byte) { b[327] = 1 },
		"original length":      func(b []byte) { binary.LittleEndian.PutUint64(b[304:], 3) },
		"observation cap":      func(b []byte) { binary.LittleEndian.PutUint32(b[296:], ObservationCap+1) },
		"missing payload flag": func(b []byte) { binary.LittleEndian.PutUint16(b[262:], clientIDFlag) },
		"unknown cid": func(b []byte) {
			binary.LittleEndian.PutUint16(b[262:], payloadFlag)
			binary.LittleEndian.PutUint32(b[288:], 3)
		},
		"postpop mode":         func(b []byte) { binary.LittleEndian.PutUint32(b[256+72+64+36:], 1) },
		"postpop cid mismatch": func(b []byte) { binary.LittleEndian.PutUint32(b[256+72+64+32:], 123) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := golden()
			mutate(b)
			if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
				t.Fatal("accepted corrupt record")
			}
		})
	}
}

func TestInvalidOutcomes(t *testing.T) {
	cases := []fixtureRecord{
		{kind: 1, flags: emptyFlag},
		{kind: 3, flags: ignoredFlag},
		{kind: 3, flags: emptyFlag | clientIDFlag},
		{kind: 3, flags: payloadFlag | clientIDFlag, payload: []byte{1, 2, 3}, original: 3},
		{kind: 1, flags: payloadFlag},
		{kind: 1, flags: payloadFlag | clientIDFlag, cid: 1, mode: 1},
		{kind: 1, flags: payloadFlag | clientIDFlag, mode: 2},
		{kind: 2, flags: payloadFlag, mode: 3},
		{kind: 2, flags: payloadFlag | clientIDFlag},
		{kind: 1, flags: ignoredFlag},
		{kind: 2, flags: ignoredFlag | clientIDFlag},
		{kind: 1, flags: ignoredFlag, original: 1},
		{kind: 3, flags: emptyFlag, original: 1},
		{kind: 1, flags: payloadFlag | ignoredFlag | clientIDFlag},
		{kind: 1, flags: capExceededFlag, original: ObservationCap},
		{kind: 2, flags: badSpanFlag | clientIDFlag},
	}
	for i, rec := range cases {
		if _, err := Read(bytes.NewReader(fixture(rec)), Capacity); err == nil {
			t.Fatalf("accepted invalid outcome %d %+v", i, rec)
		}
	}
}

func TestNativeGapsAndProvisionalPrefixes(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"missing normal close": func(b []byte) { binary.LittleEndian.PutUint64(b[88:], installed|profileMatched) },
		"not installed":        func(b []byte) { binary.LittleEndian.PutUint64(b[88:], closed) },
		"gap":                  func(b []byte) { binary.LittleEndian.PutUint64(b[88:], installed|profileMatched|closed|captureGap) },
		"overflow": func(b []byte) {
			binary.LittleEndian.PutUint64(b[88:], 15)
			binary.LittleEndian.PutUint64(b[64:], 1)
			binary.LittleEndian.PutUint64(b[72:], 1)
		},
		"thread violation without record": func(b []byte) {
			binary.LittleEndian.PutUint64(b[88:], 15)
			binary.LittleEndian.PutUint64(b[64:], 1)
			binary.LittleEndian.PutUint64(b[80:], 1)
			binary.LittleEndian.PutUint64(b[40:], 3)
		},
		"inflight record": func(b []byte) {
			binary.LittleEndian.PutUint64(b[88:], 3)
			binary.LittleEndian.PutUint64(b[32:], 6)
			binary.LittleEndian.PutUint64(b[40:], 3)
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := golden()
			mutate(b)
			r, err := Read(bytes.NewReader(b), Capacity)
			if err != nil {
				t.Fatal(err)
			}
			if r.CaptureIntegrityEligible || !r.CommittedPrefixValid || len(r.Problems) == 0 {
				t.Fatal("gap promoted to complete capture")
			}
		})
	}
	b := golden()
	r, err := Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if r.CaptureIntegrityEligible || !strings.Contains(strings.Join(r.Problems, ","), "truncated") {
		t.Fatal("truncated unused file tail not reported")
	}
	for _, rec := range []fixtureRecord{{kind: 1, flags: badSpanFlag}, {kind: 1, flags: clientIDFlag | capExceededFlag, original: ObservationCap + 1}, {kind: 2, flags: recordGapFlag, mode: 3}, {kind: 3, flags: threadViolationFlag}} {
		r, err := Read(bytes.NewReader(fixture(rec)), Capacity)
		if err != nil {
			t.Fatal(err)
		}
		if r.CaptureIntegrityEligible || r.Kinds[rec.kind-1].ErrorRecords != 1 {
			t.Fatal("native error was hidden")
		}
	}
}

func TestCommittedBoundsAndReadErrors(t *testing.T) {
	b := golden()
	if _, err := Read(bytes.NewReader(b[:len(b)-1]), Capacity); err == nil {
		t.Fatal("short underlying reader accepted")
	}
	binary.LittleEndian.PutUint64(b[24:], HeaderSize+8)
	if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
		t.Fatal("committed partial record header accepted")
	}
	binary.LittleEndian.PutUint64(b[24:], HeaderSize+64)
	if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
		t.Fatal("record extending past commit accepted")
	}
	if _, err := Read(failingReader{}, Capacity); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("reader cause lost: %v", err)
	}
}

type failingReader struct{}

func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, io.ErrClosedPipe }

func TestZeroLengthSendAndEmptyTrace(t *testing.T) {
	for _, b := range [][]byte{fixture(), fixture(fixtureRecord{kind: 1, flags: payloadFlag | clientIDFlag, mode: 1}), fixture(fixtureRecord{kind: 2, flags: payloadFlag, mode: 1})} {
		r, err := Read(bytes.NewReader(b), Capacity)
		if err != nil {
			t.Fatal(err)
		}
		if !r.CaptureIntegrityEligible {
			t.Fatal(r.Problems)
		}
	}
}

func TestRecordErrorsDoNotReadUninitializedPayload(t *testing.T) {
	b := fixture(fixtureRecord{kind: 3, flags: emptyFlag})
	r, err := Read(&limitedReader{data: b, limit: int64(len(b))}, Capacity)
	if err != nil || r.Kinds[2].Empty != 1 {
		t.Fatalf("empty optional: %v %+v", err, r)
	}
	if _, err := Read(shortReader{}, Capacity); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short ReaderAt: %v", err)
	}
}

type limitedReader struct {
	data  []byte
	limit int64
}

func (r *limitedReader) ReadAt(b []byte, at int64) (int, error) {
	if at+int64(len(b)) > r.limit {
		panic("read beyond committed record")
	}
	return bytes.NewReader(r.data).ReadAt(b, at)
}

type shortReader struct{}

func (shortReader) ReadAt([]byte, int64) (int, error) { return 0, nil }

func TestClientIgnoredModesRespectNativeCaptureGates(t *testing.T) {
	for _, mode := range []uint32{1, 3} {
		b := fixture(fixtureRecord{kind: 2, flags: ignoredFlag, mode: mode})
		if _, err := Read(bytes.NewReader(b), Capacity); err == nil {
			t.Fatalf("ignored client mode %d bypassed native capture gate", mode)
		}
	}
	for _, mode := range []uint32{0, 2, 4, ^uint32(0)} {
		b := fixture(fixtureRecord{kind: 2, flags: ignoredFlag, mode: mode})
		r, err := Read(bytes.NewReader(b), Capacity)
		if err != nil || !r.CaptureIntegrityEligible || r.Kinds[1].Ignored != 1 {
			t.Fatalf("supported ignored mode %d: %v %+v", mode, err, r)
		}
	}
	b := fixture(fixtureRecord{kind: 2, flags: recordGapFlag, mode: 3})
	r, err := Read(bytes.NewReader(b), Capacity)
	if err != nil || r.CaptureIntegrityEligible || r.Header.Errors != 1 || r.Kinds[1].ErrorRecords != 1 {
		t.Fatalf("unsupported mode3 gap: %v %+v", err, r)
	}
}
