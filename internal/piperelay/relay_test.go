package piperelay

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
	"time"
)

func wire(t *testing.T, f Frame) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := WriteFrame(&b, f); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func session(t *testing.T, frames ...Frame) []byte {
	t.Helper()
	var b []byte
	for _, f := range frames {
		b = append(b, wire(t, f)...)
	}
	return b
}
func complete(_ Report) error { return nil }

func TestGoldenWireAndOwnedBytes(t *testing.T) {
	f := Frame{Type: C2S, Sequence: 0x0102030405060708, ClientID: 0x78563412, Payload: []byte{0xaa, 0xbb, 0xcc}}
	want, _ := hex.DecodeString("4432525250303031010000000100000008070605040302011234567803000000aabbcc")
	if got := wire(t, f); !bytes.Equal(got, want) {
		t.Fatalf("wire %x", got)
	}
	input := append(append([]byte{}, want...), wire(t, Frame{Type: S2C, Sequence: 9, ClientID: math.MaxUint32, Payload: []byte{4, 5}})...)
	reader := bytes.NewReader(input)
	first, err := ReadFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	for i := range input {
		input[i] = 0
	}
	if !bytes.Equal(first.Payload, f.Payload) || second.ClientID != math.MaxUint32 || !bytes.Equal(second.Payload, []byte{4, 5}) {
		t.Fatal("read payload aliases reused input")
	}
	var written []byte
	if err := WriteFrame(writerFunc(func(b []byte) (int, error) { f.Payload[0] = 0; written = append(written, b...); return len(b), nil }), f); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, want) {
		t.Fatal("writer received borrowed caller payload")
	}
	first.Payload[0] = 9
	if second.Payload[0] != 4 {
		t.Fatal("independent reads share storage")
	}
}

func TestCodecRejectsMalformedHeadersAndTruncation(t *testing.T) {
	good := wire(t, Frame{Type: C2S, Sequence: 1, Payload: []byte{1, 2}})
	changes := map[string]func([]byte){"magic": func(b []byte) { b[0] = 'X' }, "version": func(b []byte) { binary.LittleEndian.PutUint32(b[8:], 2) },
		"unknown type": func(b []byte) { binary.LittleEndian.PutUint16(b[12:], 5) }, "zero type": func(b []byte) { binary.LittleEndian.PutUint16(b[12:], 0) },
		"reserved": func(b []byte) { binary.LittleEndian.PutUint16(b[14:], 1) }, "sequence": func(b []byte) { binary.LittleEndian.PutUint64(b[16:], 0) },
		"cap":              func(b []byte) { binary.LittleEndian.PutUint32(b[28:], MaxPayload+1) },
		"terminal payload": func(b []byte) { binary.LittleEndian.PutUint16(b[12:], uint16(Close)) },
		"terminal CID": func(b []byte) {
			binary.LittleEndian.PutUint16(b[12:], uint16(Ack))
			binary.LittleEndian.PutUint32(b[24:], 1)
			binary.LittleEndian.PutUint32(b[28:], 0)
		}}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := bytes.Clone(good)
			change(b)
			if _, err := ReadFrame(bytes.NewReader(b)); err == nil {
				t.Fatal("accepted malformed header")
			}
		})
	}
	for end := 0; end < len(good); end++ {
		if _, err := ReadFrame(bytes.NewReader(good[:end])); err == nil {
			t.Fatalf("accepted truncation %d", end)
		}
	}
	for _, f := range []Frame{{Type: 0, Sequence: 1}, {Type: C2S}, {Type: Close, Sequence: 1, ClientID: 1}, {Type: Ack, Sequence: 1, Payload: []byte{1}}, {Type: C2S, Sequence: 1, Payload: make([]byte, MaxPayload+1)}} {
		var output bytes.Buffer
		if err := WriteFrame(&output, f); err == nil || output.Len() != 0 {
			t.Fatal("invalid output frame was written")
		}
	}
	for _, kind := range []Type{C2S, S2C, Close, Ack} {
		f, err := ReadFrame(bytes.NewReader(wire(t, Frame{Type: kind, Sequence: 1})))
		if err != nil || len(f.Payload) != 0 {
			t.Fatalf("valid zero representation: %+v %v", f, err)
		}
	}
}

func TestServeEchoAndReportBeforeAck(t *testing.T) {
	data := session(t, Frame{Type: C2S, Sequence: 1, ClientID: math.MaxUint32, Payload: []byte{1, 2, 3}}, Frame{Type: S2C, Sequence: 2}, Frame{Type: C2S, Sequence: 3, Payload: []byte{4}})
	input := append(bytes.Clone(data), wire(t, Frame{Type: Close, Sequence: 4})...)
	var out bytes.Buffer
	published := false
	r, err := Serve(bytes.NewReader(input), &out, func(r Report) error {
		published = true
		if !bytes.Equal(out.Bytes(), data) || !r.InputComplete || !r.ExplicitClose || !r.EOFObserved {
			t.Fatal("report not before Ack or input incomplete")
		}
		return nil
	})
	if err != nil || !published {
		t.Fatalf("serve: %+v %v", r, err)
	}
	want := sha256.Sum256(data)
	c2s := append(wire(t, Frame{Type: C2S, Sequence: 1, ClientID: math.MaxUint32, Payload: []byte{1, 2, 3}}), wire(t, Frame{Type: C2S, Sequence: 3, Payload: []byte{4}})...)
	kindHash := sha256.Sum256(c2s)
	if r.DataFrames != 3 || r.PayloadBytes != 4 || r.FramedBytes != 100 || r.CloseSequence != 4 || r.FramedSHA256 != hex.EncodeToString(want[:]) || r.Kinds[0].Frames != 2 || r.Kinds[0].FramedSHA256 != hex.EncodeToString(kindHash[:]) || r.G0Qualified || r.GameWorkerQualified {
		t.Fatalf("report: %+v", r)
	}
	if !bytes.Equal(out.Bytes()[:len(data)], data) {
		t.Fatal("echo altered header or payload")
	}
	ack, err := ReadFrame(bytes.NewReader(out.Bytes()[len(data):]))
	if err != nil || ack.Type != Ack || ack.Sequence != 4 || out.Len() != len(data)+HeaderSize {
		t.Fatalf("Ack: %+v %v", ack, err)
	}
}

func TestSessionRejectsOrderTerminalAndTrailingInputs(t *testing.T) {
	data := Frame{Type: C2S, Sequence: 1, Payload: []byte{1}}
	inputs := map[string][]byte{
		"initial EOF": nil, "no Close": wire(t, data), "initial sequence": wire(t, Frame{Type: C2S, Sequence: 2}),
		"duplicate": session(t, data, data), "skipped": session(t, data, Frame{Type: S2C, Sequence: 3}),
		"Ack": wire(t, Frame{Type: Ack, Sequence: 1}), "wrong Close sequence": session(t, data, Frame{Type: Close, Sequence: 3}),
		"trailing":     append(wire(t, Frame{Type: Close, Sequence: 1}), 0),
		"second Close": session(t, Frame{Type: Close, Sequence: 1}, Frame{Type: Close, Sequence: 2}),
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			called := false
			var out bytes.Buffer
			r, err := Serve(bytes.NewReader(input), &out, func(Report) error { called = true; return nil })
			if err == nil || called || r != (Report{}) {
				t.Fatalf("partial session published: %+v %v", r, err)
			}
		})
	}
	if _, err := Serve(bytes.NewReader(nil), io.Discard, nil); err == nil {
		t.Fatal("missing report callback accepted")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
func TestIOAndPublicationErrors(t *testing.T) {
	problem := errors.New("synthetic failure")
	closeWire := wire(t, Frame{Type: Close, Sequence: 1})
	for _, input := range []io.Reader{errorReader{problem}, io.MultiReader(bytes.NewReader(closeWire), errorReader{problem}), io.MultiReader(bytes.NewReader(wire(t, Frame{Type: C2S, Sequence: 1, Payload: []byte{1}})[:HeaderSize]), errorReader{problem})} {
		if _, err := Serve(input, io.Discard, complete); !errors.Is(err, problem) {
			t.Fatalf("lost read error: %v", err)
		}
	}
	for _, ackFailure := range []bool{false, true} {
		input := closeWire
		if !ackFailure {
			input = session(t, Frame{Type: C2S, Sequence: 1}, Frame{Type: Close, Sequence: 2})
		}
		called := false
		r, err := Serve(bytes.NewReader(input), writerFunc(func([]byte) (int, error) { return 0, problem }), func(Report) error { called = true; return nil })
		if !errors.Is(err, problem) || called != ackFailure || r != (Report{}) {
			t.Fatalf("write failure changed input completion semantics: %+v %v %v", r, err, called)
		}
	}
	var out bytes.Buffer
	if _, err := Serve(bytes.NewReader(closeWire), &out, func(Report) error { return problem }); !errors.Is(err, problem) || out.Len() != 0 {
		t.Fatalf("publication failure acknowledged: %v", err)
	}
	if err := WriteFrame(writerFunc(func(p []byte) (int, error) { return len(p) - 1, nil }), Frame{Type: C2S, Sequence: 1}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
}

func repeatedInput(t *testing.T, count int, payload []byte, finish bool, extra []byte) io.Reader {
	t.Helper()
	readers := make([]io.Reader, 0, count*2+2)
	for i := 0; i < count; i++ {
		h := wire(t, Frame{Type: C2S, Sequence: uint64(i + 1)})
		binary.LittleEndian.PutUint32(h[28:], uint32(len(payload)))
		readers = append(readers, bytes.NewReader(h), bytes.NewReader(payload))
	}
	if extra != nil {
		readers = append(readers, bytes.NewReader(wire(t, Frame{Type: S2C, Sequence: uint64(count + 1), Payload: extra})))
		count++
	}
	if finish {
		readers = append(readers, bytes.NewReader(wire(t, Frame{Type: Close, Sequence: uint64(count + 1)})))
	}
	return io.MultiReader(readers...)
}
func TestResourceCapsAtAndBeyondBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		count   int
		payload []byte
		extra   []byte
		valid   bool
	}{
		{"frame maximum", MaxDataFrames, nil, nil, true}, {"too many frames", MaxDataFrames + 1, nil, nil, false},
		{"byte maximum", MaxDataBytes / MaxPayload, make([]byte, MaxPayload), nil, true},
		{"too many bytes", MaxDataBytes / MaxPayload, make([]byte, MaxPayload), []byte{1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			r, err := Serve(repeatedInput(t, test.count, test.payload, true, test.extra), io.Discard, func(Report) error { called = true; return nil })
			if (err == nil) != test.valid || called != test.valid {
				t.Fatalf("cap result: %+v %v", r, err)
			}
			if test.valid && r.DataFrames != uint64(test.count) {
				t.Fatal("wrong frame count")
			}
		})
	}
}

func TestFullFramePipeRoundTripDoesNotDeadlock(t *testing.T) {
	in, send := io.Pipe()
	receive, out := io.Pipe()
	closeAll := func() { _ = in.Close(); _ = send.Close(); _ = receive.Close(); _ = out.Close() }
	defer closeAll()
	done := make(chan error, 2)
	payload := bytes.Repeat([]byte{0x5a}, MaxPayload)
	go func() { _, err := Serve(in, out, complete); _ = out.CloseWithError(err); done <- err }()
	go func() {
		if err := WriteFrame(send, Frame{Type: C2S, Sequence: 1, ClientID: 7, Payload: payload}); err != nil {
			done <- err
			return
		}
		f, err := ReadFrame(receive)
		if err != nil || f.Type != C2S || f.Sequence != 1 || f.ClientID != 7 || !bytes.Equal(f.Payload, payload) {
			done <- fmt.Errorf("pipe echo mismatch: %v", err)
			return
		}
		if err = WriteFrame(send, Frame{Type: Close, Sequence: 2}); err != nil {
			done <- err
			return
		}
		_ = send.Close()
		f, err = ReadFrame(receive)
		if err != nil || f.Type != Ack || f.Sequence != 2 {
			done <- fmt.Errorf("pipe Ack mismatch: %v", err)
			return
		}
		var one [1]byte
		if n, err := receive.Read(one[:]); n != 0 || err != io.EOF {
			done <- fmt.Errorf("pipe did not end after Ack: %v", err)
			return
		}
		done <- nil
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var workerErr error
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				workerErr = errors.Join(workerErr, err)
				closeAll()
			}
		case <-timer.C:
			closeAll()
			for j := i; j < 2; j++ {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("pipe worker did not stop")
				}
			}
			t.Fatal("full-frame bidirectional pipe deadlock")
		}
	}
	if workerErr != nil {
		t.Fatal(workerErr)
	}
}
