package protocol

import (
	"encoding/binary"
	"errors"
)

// frameHeaderSize is the fixed prefix on every binary frame: an 8-byte stream
// id followed by one flags byte.
const frameHeaderSize = 9

// MaxFrameData is the largest payload carried in one frame. 32 KiB matches the
// buffer io.Copy uses, so bodies are forwarded without extra copying or
// re-chunking.
const MaxFrameData = 32 << 10

// Frame flags.
const (
	// flagEnd marks the final frame of a stream.
	flagEnd byte = 1 << 0
)

// ErrShortFrame reports a binary frame that is too small to be valid.
var ErrShortFrame = errors.New("protocol: binary frame shorter than its header")

// Frame is one chunk of a request or response body.
//
// Bodies travel as binary frames rather than inside JSON because base64 would
// inflate every byte of every download by a third, and because a frame can be
// forwarded straight into an io.Writer without re-encoding.
type Frame struct {
	// Stream matches the Request that opened this body stream.
	Stream uint64
	// End marks the last frame; a frame may be both final and empty.
	End bool
	// Data is the payload, at most MaxFrameData bytes.
	Data []byte
}

// EncodeFrame serialises a frame.
func EncodeFrame(f Frame) []byte {
	buf := make([]byte, frameHeaderSize+len(f.Data))
	binary.BigEndian.PutUint64(buf[:8], f.Stream)
	if f.End {
		buf[8] = flagEnd
	}
	copy(buf[frameHeaderSize:], f.Data)
	return buf
}

// DecodeFrame parses a frame. The returned Data aliases the input buffer, so
// callers that retain it past the read loop must copy it.
func DecodeFrame(buf []byte) (Frame, error) {
	if len(buf) < frameHeaderSize {
		return Frame{}, ErrShortFrame
	}
	return Frame{
		Stream: binary.BigEndian.Uint64(buf[:8]),
		End:    buf[8]&flagEnd != 0,
		Data:   buf[frameHeaderSize:],
	}, nil
}
