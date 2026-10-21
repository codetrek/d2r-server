package guestingress

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"
	"testing"
)

func TestGoldenBoundaries(t *testing.T) {
	message := Message{ClientID: 0x78563412, Payload: []byte{0x16, 0x80, 0xff}}
	queue, err := EncodeQueueEntry(message)
	if err != nil {
		t.Fatal(err)
	}
	if expected := []byte{3, 0, 0x12, 0x34, 0x56, 0x78, 0x16, 0x80, 0xff}; !bytes.Equal(queue, expected) {
		t.Fatalf("queue entry = %x, want %x", queue, expected)
	}
	receiver, length, err := EncodeReceiverBuffer(message)
	if err != nil {
		t.Fatal(err)
	}
	if expected := []byte{0x12, 0x34, 0x56, 0x78, 0x16, 0x80, 0xff}; !bytes.Equal(receiver, expected) || length != 3 {
		t.Fatalf("receiver = %x, length = %d; want %x, 3", receiver, length, expected)
	}
	decodedQueue, err := DecodeQueueEntry(queue)
	if err != nil {
		t.Fatal(err)
	}
	decodedReceiver, err := DecodeReceiverBuffer(receiver, length)
	if err != nil {
		t.Fatal(err)
	}
	assertMessage(t, decodedQueue, message)
	assertMessage(t, decodedReceiver, message)
}

func TestRoundTripBoundaryLengthsAndIdentities(t *testing.T) {
	for _, size := range []int{0, 1, 511, 512, MaxQueuePayload} {
		for _, clientID := range []uint32{0, 1, math.MaxUint32} {
			t.Run(strconv.Itoa(size)+"/"+strconv.FormatUint(uint64(clientID), 10), func(t *testing.T) {
				payload := make([]byte, size)
				for i := range payload {
					payload[i] = byte(i)
				}
				message := Message{ClientID: clientID, Payload: payload}
				queue, err := EncodeQueueEntry(message)
				if err != nil {
					t.Fatal(err)
				}
				decodedQueue, err := DecodeQueueEntry(queue)
				if err != nil {
					t.Fatal(err)
				}
				assertMessage(t, decodedQueue, message)
				receiver, length, err := EncodeReceiverBuffer(message)
				if err != nil {
					t.Fatal(err)
				}
				decodedReceiver, err := DecodeReceiverBuffer(receiver, length)
				if err != nil {
					t.Fatal(err)
				}
				assertMessage(t, decodedReceiver, message)
			})
		}
	}
}

func TestQueueLengthIsNotReceiverLimit(t *testing.T) {
	message := Message{ClientID: math.MaxUint32, Payload: bytes.Repeat([]byte{0xa5}, MaxQueuePayload+1)}
	if _, err := EncodeQueueEntry(message); err == nil {
		t.Fatal("queue payload overflowing uint16 was accepted")
	}
	buffer, length, err := EncodeReceiverBuffer(message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReceiverBuffer(buffer, length)
	if err != nil {
		t.Fatal(err)
	}
	assertMessage(t, decoded, message)
}

func TestQueueRequiresOneExactEntry(t *testing.T) {
	golden := []byte{3, 0, 0x12, 0x34, 0x56, 0x78, 0x16, 0x80, 0xff}
	for size := 0; size < len(golden); size++ {
		if _, err := DecodeQueueEntry(golden[:size]); err == nil {
			t.Fatalf("accepted queue truncated to %d bytes", size)
		}
	}
	for _, extra := range [][]byte{{0}, golden} {
		input := append(bytes.Clone(golden), extra...)
		if _, err := DecodeQueueEntry(input); err == nil {
			t.Fatalf("accepted trailing queue bytes %x", extra)
		}
	}
	for _, declared := range []uint16{0, 2, 4, math.MaxUint16} {
		input := bytes.Clone(golden)
		binary.LittleEndian.PutUint16(input, declared)
		if _, err := DecodeQueueEntry(input); err == nil {
			t.Fatalf("accepted queue payload length mismatch %d", declared)
		}
	}
	if _, err := DecodeQueueEntry([]byte{0xff, 0xff, 0, 0, 0, 0}); err == nil {
		t.Fatal("accepted truncated maximum-length queue entry")
	}
}

func TestReceiverRequiresExactSeparateLength(t *testing.T) {
	golden := []byte{0x12, 0x34, 0x56, 0x78, 0x16, 0x80, 0xff}
	for size := 0; size < len(golden); size++ {
		if _, err := DecodeReceiverBuffer(golden[:size], 3); err == nil {
			t.Fatalf("accepted receiver truncated to %d bytes", size)
		}
	}
	if _, err := DecodeReceiverBuffer(append(bytes.Clone(golden), 0), 3); err == nil {
		t.Fatal("accepted receiver trailing byte")
	}
	for _, declared := range []uint32{0, 2, 4, 7, math.MaxUint32} {
		if _, err := DecodeReceiverBuffer(golden, declared); err == nil {
			t.Fatalf("accepted receiver length mismatch %d", declared)
		}
	}
	if _, err := DecodeReceiverBuffer(golden[:3], 0); err == nil {
		t.Fatal("accepted zero-payload receiver with a truncated prefix")
	}
}

func TestReceiverLengthArithmetic(t *testing.T) {
	if _, err := receiverSize(uint64(math.MaxUint32) + 1); err == nil {
		t.Fatal("accepted receiver payload length overflowing uint32")
	}
	size, err := receiverSize(math.MaxUint32)
	if strconv.IntSize == 32 {
		if err == nil {
			t.Fatal("accepted receiver size overflowing platform int")
		}
		return
	}
	if err != nil || uint64(size) != uint64(math.MaxUint32)+4 {
		t.Fatalf("maximum receiver size = %d, %v", size, err)
	}
}

func TestBoundariesOwnTheirBytes(t *testing.T) {
	message := Message{ClientID: 42, Payload: []byte{1, 2, 3}}
	queueA, err := EncodeQueueEntry(message)
	if err != nil {
		t.Fatal(err)
	}
	queueB, err := EncodeQueueEntry(message)
	if err != nil {
		t.Fatal(err)
	}
	receiverA, length, err := EncodeReceiverBuffer(message)
	if err != nil {
		t.Fatal(err)
	}
	receiverB, _, err := EncodeReceiverBuffer(message)
	if err != nil {
		t.Fatal(err)
	}
	message.Payload[0] = 9
	if queueA[6] != 1 || receiverA[4] != 1 {
		t.Fatal("encoder retained its input payload")
	}
	queueA[6] = 8
	receiverA[4] = 7
	if queueB[6] != 1 || receiverB[4] != 1 {
		t.Fatal("independent encodes share payload storage")
	}
	decodedQueueA, err := DecodeQueueEntry(queueB)
	if err != nil {
		t.Fatal(err)
	}
	decodedQueueB, err := DecodeQueueEntry(queueB)
	if err != nil {
		t.Fatal(err)
	}
	decodedReceiverA, err := DecodeReceiverBuffer(receiverB, length)
	if err != nil {
		t.Fatal(err)
	}
	decodedReceiverB, err := DecodeReceiverBuffer(receiverB, length)
	if err != nil {
		t.Fatal(err)
	}
	queueB[6], receiverB[4] = 6, 5
	if decodedQueueA.Payload[0] != 1 || decodedReceiverA.Payload[0] != 1 {
		t.Fatal("decoder retained its input buffer")
	}
	decodedQueueA.Payload[0], decodedReceiverA.Payload[0] = 4, 3
	if decodedQueueB.Payload[0] != 1 || decodedReceiverB.Payload[0] != 1 {
		t.Fatal("independent decodes share payload storage")
	}
}

func assertMessage(t *testing.T, actual, expected Message) {
	t.Helper()
	if actual.ClientID != expected.ClientID || !bytes.Equal(actual.Payload, expected.Payload) {
		t.Fatalf("message = {%d, %x}, want {%d, %x}", actual.ClientID, actual.Payload, expected.ClientID, expected.Payload)
	}
}
