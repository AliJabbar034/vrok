package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Keepalive and deadline tuning. A tunnel often sits idle between visitors, so
// both ends ping; a peer that stops answering is detected within pongWait
// rather than leaving a dead tunnel advertised as live.
const (
	pingInterval = 20 * time.Second
	pongWait     = 60 * time.Second
	writeWait    = 30 * time.Second
)

// MaxMessageSize caps one inbound WebSocket message. Body frames are at most
// MaxFrameData plus a header; the largest control message is a request
// envelope carrying a visitor's headers, which net/http already bounds at
// 1 MiB before JSON escaping. Without a cap, gorilla/websocket buffers a
// message of any size, so one peer could exhaust the other's memory with a
// single frame.
const MaxMessageSize = 4 << 20

// ErrClosed is returned once a connection has been closed.
var ErrClosed = errors.New("protocol: connection closed")

// Conn is a multiplexed, concurrency-safe view of one WebSocket connection.
//
// A WebSocket allows only one writer at a time, which is easy to violate once
// several HTTP streams share the connection. Serialising every write behind
// one mutex here means no caller has to remember that rule.
type Conn struct {
	ws *websocket.Conn

	writeMu sync.Mutex
	closed  bool
}

// NewConn wraps a WebSocket connection.
func NewConn(ws *websocket.Conn) *Conn {
	c := &Conn{ws: ws}
	ws.SetReadLimit(MaxMessageSize)
	// Every inbound message, including a pong, proves the peer is alive and
	// extends the deadline.
	ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	return c
}

// Send writes a control message.
func (c *Conn) Send(t Type, payload any) error {
	data, err := Encode(t, payload)
	if err != nil {
		return err
	}
	return c.write(websocket.TextMessage, data)
}

// SendFrame writes one body frame. Data longer than MaxFrameData must be split
// by the caller, or sent with Copy.
func (c *Conn) SendFrame(f Frame) error {
	if len(f.Data) > MaxFrameData {
		return fmt.Errorf("protocol: frame of %d bytes exceeds the %d byte limit", len(f.Data), MaxFrameData)
	}
	return c.write(websocket.BinaryMessage, EncodeFrame(f))
}

func (c *Conn) write(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.ws.WriteMessage(messageType, data)
}

// Receive reads the next message. Exactly one of the results is non-nil: a
// control envelope or a body frame.
//
// A frame's Data aliases an internal buffer that is reused by the next call,
// so it must be consumed or copied before reading again.
func (c *Conn) Receive() (*Envelope, *Frame, error) {
	for {
		messageType, data, err := c.ws.ReadMessage()
		if err != nil {
			return nil, nil, err
		}
		c.ws.SetReadDeadline(time.Now().Add(pongWait))

		switch messageType {
		case websocket.TextMessage:
			envelope, err := Decode(data)
			if err != nil {
				return nil, nil, err
			}
			return &envelope, nil, nil

		case websocket.BinaryMessage:
			frame, err := DecodeFrame(data)
			if err != nil {
				return nil, nil, err
			}
			return nil, &frame, nil

		default:
			// Control frames are handled by the WebSocket layer; anything
			// else is not part of this protocol and is ignored.
			continue
		}
	}
}

// Keepalive pings the peer until ctx is cancelled or the connection fails.
func (c *Conn) Keepalive(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.writeMu.Lock()
			if c.closed {
				c.writeMu.Unlock()
				return
			}
			c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			err := c.ws.WriteMessage(websocket.PingMessage, nil)
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// Close shuts the connection down, attempting a clean WebSocket close first so
// the peer learns this was deliberate rather than a network failure.
func (c *Conn) Close() error {
	c.writeMu.Lock()
	if c.closed {
		c.writeMu.Unlock()
		return nil
	}
	c.closed = true
	c.ws.SetWriteDeadline(time.Now().Add(time.Second))
	c.ws.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	c.writeMu.Unlock()
	return c.ws.Close()
}

// Copy streams src to the peer as body frames, terminated by an end frame.
// The end frame is sent even when the read fails, so the receiving side is
// never left waiting for a stream that will not continue.
func (c *Conn) Copy(stream uint64, src io.Reader) error {
	buf := make([]byte, MaxFrameData)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if err := c.SendFrame(Frame{Stream: stream, Data: buf[:n]}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return c.SendFrame(Frame{Stream: stream, End: true})
			}
			// Signal the end anyway; the peer sees a truncated body, which is
			// the same outcome as a dropped HTTP connection.
			c.SendFrame(Frame{Stream: stream, End: true})
			return readErr
		}
	}
}

// IsExpectedClose reports whether err is a normal end of connection rather
// than a fault worth logging.
func IsExpectedClose(err error) bool {
	if err == nil || errors.Is(err, ErrClosed) || errors.Is(err, io.EOF) {
		return true
	}
	return websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
		websocket.CloseAbnormalClosure)
}
