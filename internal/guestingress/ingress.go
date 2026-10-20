// Package guestingress encodes individual guest server ingress boundaries.
// Channel selection and authenticated session binding belong to the caller.
package guestingress

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

const (
	// MaxQueuePayload is the limit of a queue entry's uint16 length field.
	MaxQueuePayload    = math.MaxUint16
	queueHeaderSize    = 6
	receiverHeaderSize = 4
)

// Message contains a guest connection identity and an opaque guest payload.
type Message struct {
	ClientID uint32
	Payload  []byte
}

// EncodeQueueEntry returns one owned [u16 payload length | u32 client ID | payload] entry.
func EncodeQueueEntry(message Message) ([]byte, error) {
	if len(message.Payload) > MaxQueuePayload {
		return nil, fmt.Errorf("guest ingress queue payload length %d exceeds uint16 limit %d", len(message.Payload), MaxQueuePayload)
	}
	entry := make([]byte, queueHeaderSize+len(message.Payload))
	binary.LittleEndian.PutUint16(entry, uint16(len(message.Payload)))
	binary.LittleEndian.PutUint32(entry[2:], message.ClientID)
	copy(entry[queueHeaderSize:], message.Payload)
	return entry, nil
}

// DecodeQueueEntry requires exactly one entry and returns an owned payload copy.
func DecodeQueueEntry(entry []byte) (Message, error) {
	if len(entry) < queueHeaderSize {
		return Message{}, fmt.Errorf("guest ingress queue entry has %d bytes, requires at least %d", len(entry), queueHeaderSize)
	}
	payloadLength := int(binary.LittleEndian.Uint16(entry))
	if len(entry) != queueHeaderSize+payloadLength {
		return Message{}, fmt.Errorf("guest ingress queue entry has %d bytes, length field requires %d", len(entry), queueHeaderSize+payloadLength)
	}
	return Message{ClientID: binary.LittleEndian.Uint32(entry[2:]), Payload: bytes.Clone(entry[queueHeaderSize:])}, nil
}

// EncodeReceiverBuffer returns an owned [u32 client ID | payload] buffer and the
// separate payload length. The length excludes the connection prefix, as the
// upstream rewrite retains four prefix bytes while passing length 25:
// https://github.com/jhaizhou-ops/d2r-mobile/blob/120fe55c02a5633476998a44f8c3c3c809fa48b0/dm/host/diag.c#L1339
func EncodeReceiverBuffer(message Message) ([]byte, uint32, error) {
	size, err := receiverSize(uint64(len(message.Payload)))
	if err != nil {
		return nil, 0, err
	}
	buffer := make([]byte, size)
	binary.LittleEndian.PutUint32(buffer, message.ClientID)
	copy(buffer[receiverHeaderSize:], message.Payload)
	return buffer, uint32(len(message.Payload)), nil
}

// DecodeReceiverBuffer requires exactly the prefix plus the separate payload
// length and returns an owned payload copy.
func DecodeReceiverBuffer(buffer []byte, payloadLength uint32) (Message, error) {
	size, err := receiverSize(uint64(payloadLength))
	if err != nil {
		return Message{}, err
	}
	if len(buffer) != size {
		return Message{}, fmt.Errorf("guest ingress receiver buffer has %d bytes, payload length %d requires %d", len(buffer), payloadLength, size)
	}
	return Message{ClientID: binary.LittleEndian.Uint32(buffer), Payload: bytes.Clone(buffer[receiverHeaderSize:])}, nil
}

func receiverSize(payloadLength uint64) (int, error) {
	if payloadLength > math.MaxUint32 {
		return 0, fmt.Errorf("guest ingress receiver payload length %d exceeds uint32 limit", payloadLength)
	}
	size := receiverHeaderSize + payloadLength
	if size > uint64(math.MaxInt) {
		return 0, fmt.Errorf("guest ingress receiver buffer size %d exceeds platform int limit", size)
	}
	return int(size), nil
}
