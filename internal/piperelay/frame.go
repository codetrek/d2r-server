// Package piperelay implements an owned-child pipe experiment. Its envelope is
// independent of game protocol, authentication, message schemas and wire limits.
package piperelay

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	HeaderSize    = 32
	MaxPayload    = 1 << 20
	MaxDataFrames = 65536
	MaxDataBytes  = 64 << 20
)

type Type uint16

const (
	C2S   Type = 1
	S2C   Type = 2
	Close Type = 3
	Ack   Type = 4
)

type Frame struct {
	Type     Type
	Sequence uint64
	ClientID uint32
	Payload  []byte
}

// ReadFrame owns the complete returned payload, independent of prior frames or
// buffers reused by the reader. Sequence continuity belongs to Serve.
func ReadFrame(input io.Reader) (Frame, error) {
	frame, _, err := readWire(input)
	return frame, err
}

func readWire(input io.Reader) (Frame, []byte, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(input, h[:]); err != nil {
		return Frame{}, nil, fmt.Errorf("read relay header: %w", err)
	}
	if string(h[:8]) != "D2RRP001" || binary.LittleEndian.Uint32(h[8:]) != 1 || binary.LittleEndian.Uint16(h[14:]) != 0 {
		return Frame{}, nil, fmt.Errorf("invalid relay magic, version or reserved flags")
	}
	frame := Frame{Type: Type(binary.LittleEndian.Uint16(h[12:])), Sequence: binary.LittleEndian.Uint64(h[16:]), ClientID: binary.LittleEndian.Uint32(h[24:])}
	length := binary.LittleEndian.Uint32(h[28:])
	if err := validate(frame.Type, frame.Sequence, frame.ClientID, uint64(length)); err != nil {
		return Frame{}, nil, err
	}
	wire := make([]byte, HeaderSize+int(length))
	copy(wire, h[:])
	frame.Payload = wire[HeaderSize:]
	if _, err := io.ReadFull(input, frame.Payload); err != nil {
		return Frame{}, nil, fmt.Errorf("read relay payload: %w", err)
	}
	return frame, wire, nil
}

// WriteFrame copies the complete frame before the first write, so caller-owned
// payload storage is never retained or handed to the output writer.
func WriteFrame(output io.Writer, frame Frame) error {
	if err := validate(frame.Type, frame.Sequence, frame.ClientID, uint64(len(frame.Payload))); err != nil {
		return err
	}
	wire := make([]byte, HeaderSize+len(frame.Payload))
	copy(wire, "D2RRP001")
	binary.LittleEndian.PutUint32(wire[8:], 1)
	binary.LittleEndian.PutUint16(wire[12:], uint16(frame.Type))
	binary.LittleEndian.PutUint64(wire[16:], frame.Sequence)
	binary.LittleEndian.PutUint32(wire[24:], frame.ClientID)
	binary.LittleEndian.PutUint32(wire[28:], uint32(len(frame.Payload)))
	copy(wire[HeaderSize:], frame.Payload)
	return writeWire(output, wire)
}

func validate(kind Type, sequence uint64, cid uint32, length uint64) error {
	if sequence == 0 {
		return fmt.Errorf("relay sequence must be nonzero")
	}
	if kind < C2S || kind > Ack {
		return fmt.Errorf("unknown relay frame type %d", kind)
	}
	if length > MaxPayload {
		return fmt.Errorf("relay payload length %d exceeds pipe resource cap %d", length, MaxPayload)
	}
	if kind >= Close && (cid != 0 || length != 0) {
		return fmt.Errorf("terminal relay frame must have zero client ID and payload length")
	}
	return nil
}

func writeWire(output io.Writer, wire []byte) error {
	n, err := output.Write(wire)
	if err != nil {
		return fmt.Errorf("write relay frame: %w", err)
	}
	if n != len(wire) {
		return fmt.Errorf("write relay frame: %w", io.ErrShortWrite)
	}
	return nil
}
