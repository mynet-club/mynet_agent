package hub

import (
	"encoding/binary"
	"fmt"
	"io"
)

// utc/2 wire primitives, mirroring mynet_utc/include/mynet/mynet_hub_protocol.h.
// Every integer is network byte order. TLV entries are canonical: strictly
// increasing 15-bit types, no duplicates. Unknown control fields fail closed.
const (
	frameVersion     = 2
	frameHeaderSize  = 12
	framePayloadMax  = 65535
	frameTypeControl = 1

	tlvHeaderSize = 6
	tlvTypeMax    = 0x7fff
)

type controlFrame struct {
	Type     uint8
	Flags    uint16
	StreamID uint32
	Payload  []byte
}

func encodeControlFrame(streamID uint32, payload []byte) ([]byte, error) {
	if len(payload) > framePayloadMax {
		return nil, fmt.Errorf("utc/2 payload exceeds %d bytes", framePayloadMax)
	}
	frame := make([]byte, frameHeaderSize+len(payload))
	frame[0] = frameVersion
	frame[1] = frameTypeControl
	binary.BigEndian.PutUint16(frame[2:], 0)
	binary.BigEndian.PutUint32(frame[4:], streamID)
	binary.BigEndian.PutUint32(frame[8:], uint32(len(payload)))
	copy(frame[frameHeaderSize:], payload)
	return frame, nil
}

func readControlFrame(reader io.Reader) (controlFrame, error) {
	var frame controlFrame
	var header [frameHeaderSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return frame, err
	}
	if header[0] != frameVersion {
		return frame, fmt.Errorf("utc/2 version %d is unsupported", header[0])
	}
	if header[1] != frameTypeControl {
		return frame, fmt.Errorf("utc/2 type %d is not CONTROL", header[1])
	}
	flags := binary.BigEndian.Uint16(header[2:])
	if flags != 0 {
		return frame, fmt.Errorf("utc/2 flags 0x%04x are unsupported", flags)
	}
	length := binary.BigEndian.Uint32(header[8:])
	if length > framePayloadMax {
		return frame, fmt.Errorf("utc/2 payload length %d exceeds limit", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return frame, err
	}
	frame.Type = header[1]
	frame.Flags = flags
	frame.StreamID = binary.BigEndian.Uint32(header[4:])
	frame.Payload = payload
	return frame, nil
}

type tlvWriter struct {
	buffer   []byte
	previous uint16
	count    int
}

func newTLVWriter(capacity int) *tlvWriter {
	return &tlvWriter{buffer: make([]byte, 0, capacity)}
}

func (w *tlvWriter) add(typ uint16, value []byte) error {
	if typ == 0 || typ > tlvTypeMax {
		return fmt.Errorf("utc/2 TLV type %d is out of range", typ)
	}
	if w.count > 0 && typ <= w.previous {
		return fmt.Errorf("utc/2 TLV type %d is not increasing", typ)
	}
	if len(value) > framePayloadMax {
		return fmt.Errorf("utc/2 TLV value is too large")
	}
	var header [tlvHeaderSize]byte
	binary.BigEndian.PutUint16(header[0:], typ)
	binary.BigEndian.PutUint32(header[2:], uint32(len(value)))
	w.buffer = append(w.buffer, header[:]...)
	w.buffer = append(w.buffer, value...)
	w.previous = typ
	w.count++
	return nil
}

func (w *tlvWriter) addU8(typ uint16, value uint8) error { return w.add(typ, []byte{value}) }

func (w *tlvWriter) addU16(typ uint16, value uint16) error {
	var raw [2]byte
	binary.BigEndian.PutUint16(raw[:], value)
	return w.add(typ, raw[:])
}

func (w *tlvWriter) addU32(typ uint16, value uint32) error {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], value)
	return w.add(typ, raw[:])
}

func (w *tlvWriter) addU64(typ uint16, value uint64) error {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	return w.add(typ, raw[:])
}

type tlvValue struct {
	Type     uint16
	Critical bool
	Value    []byte
}

func parseTLVs(raw []byte) ([]tlvValue, error) {
	var entries []tlvValue
	var previous uint16
	offset := 0
	for offset < len(raw) {
		if len(raw)-offset < tlvHeaderSize {
			return nil, fmt.Errorf("utc/2 TLV header truncated")
		}
		wireType := binary.BigEndian.Uint16(raw[offset:])
		typ := wireType & tlvTypeMax
		length := binary.BigEndian.Uint32(raw[offset+2:])
		if typ == 0 {
			return nil, fmt.Errorf("utc/2 TLV type 0 is invalid")
		}
		if wireType&0x8000 != 0 {
			return nil, fmt.Errorf("utc/2 TLV type %d is critical and unsupported", typ)
		}
		if len(entries) > 0 && typ <= previous {
			return nil, fmt.Errorf("utc/2 TLV type %d is not increasing", typ)
		}
		offset += tlvHeaderSize
		if uint64(length) > uint64(len(raw)-offset) {
			return nil, fmt.Errorf("utc/2 TLV value truncated")
		}
		entries = append(entries, tlvValue{
			Type:     typ,
			Critical: wireType&0x8000 != 0,
			Value:    raw[offset : offset+int(length)],
		})
		previous = typ
		offset += int(length)
	}
	return entries, nil
}
