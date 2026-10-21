package piperelay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

type KindSummary struct {
	Kind         string `json:"kind"`
	Frames       uint64 `json:"frames"`
	PayloadBytes uint64 `json:"payload_bytes"`
	FramedSHA256 string `json:"framed_sha256"`
}

type Report struct {
	SchemaVersion       int            `json:"schema_version"`
	Scope               string         `json:"scope"`
	DigestEncoding      string         `json:"digest_encoding"`
	InputComplete       bool           `json:"input_complete"`
	ExplicitClose       bool           `json:"explicit_close"`
	EOFObserved         bool           `json:"eof_observed"`
	CloseSequence       uint64         `json:"close_sequence"`
	DataFrames          uint64         `json:"data_frames"`
	PayloadBytes        uint64         `json:"payload_bytes"`
	FramedBytes         uint64         `json:"framed_bytes"`
	FramedSHA256        string         `json:"framed_sha256"`
	Kinds               [2]KindSummary `json:"kinds"`
	G0Qualified         bool           `json:"g0_qualified"`
	GameWorkerQualified bool           `json:"game_worker_qualified"`
}

// Serve buffers every complete frame before echoing it. After Close it requires
// input EOF, then invokes complete before writing Ack. A completed input report
// does not prove that the parent received that Ack. On failure no partial report
// is returned; an already published complete input report remains truthful if
// the final Ack write fails.
func Serve(input io.Reader, output io.Writer, complete func(Report) error) (Report, error) {
	if complete == nil {
		return Report{}, fmt.Errorf("relay requires a completion callback")
	}
	r := Report{SchemaVersion: 1, Scope: "owned pipe input completion only; no remote authentication, game protocol, authority or multiplayer qualification",
		DigestEncoding: "SHA-256 of exact 32-byte data headers followed by complete payloads in input order; Close and Ack excluded; per-kind digests preserve that kind's order",
		Kinds:          [2]KindSummary{{Kind: "c2s"}, {Kind: "s2c"}}}
	all := sha256.New()
	kinds := [2]hash.Hash{sha256.New(), sha256.New()}
	for next := uint64(1); ; next++ {
		f, wire, err := readWire(input)
		if err != nil {
			return Report{}, err
		}
		if f.Sequence != next {
			return Report{}, fmt.Errorf("relay sequence %d differs from expected %d", f.Sequence, next)
		}
		if f.Type == Ack {
			return Report{}, fmt.Errorf("relay input cannot contain Ack")
		}
		if f.Type == Close {
			var extra [1]byte
			if n, err := io.ReadFull(input, extra[:]); n != 0 {
				return Report{}, fmt.Errorf("relay has trailing bytes after Close")
			} else if err != io.EOF {
				return Report{}, fmt.Errorf("read relay EOF after Close: %w", err)
			}
			r.InputComplete, r.ExplicitClose, r.EOFObserved = true, true, true
			r.CloseSequence = f.Sequence
			r.FramedSHA256 = hex.EncodeToString(all.Sum(nil))
			for i := range r.Kinds {
				r.Kinds[i].FramedSHA256 = hex.EncodeToString(kinds[i].Sum(nil))
			}
			if err := complete(r); err != nil {
				return Report{}, fmt.Errorf("publish complete relay input report: %w", err)
			}
			if err := WriteFrame(output, Frame{Type: Ack, Sequence: f.Sequence}); err != nil {
				return Report{}, err
			}
			return r, nil
		}
		if r.DataFrames == MaxDataFrames || uint64(len(f.Payload)) > MaxDataBytes-r.PayloadBytes {
			return Report{}, fmt.Errorf("relay data exceeds bounded frame or payload-byte resources")
		}
		if err := writeWire(output, wire); err != nil {
			return Report{}, err
		}
		r.DataFrames++
		r.PayloadBytes += uint64(len(f.Payload))
		r.FramedBytes += uint64(len(wire))
		k := &r.Kinds[f.Type-1]
		k.Frames++
		k.PayloadBytes += uint64(len(f.Payload))
		_, _ = all.Write(wire)
		_, _ = kinds[f.Type-1].Write(wire)
	}
}
