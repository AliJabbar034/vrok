package protocol_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/protocol"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []protocol.Frame{
		{Stream: 1, Data: []byte("hello")},
		{Stream: 0, Data: nil},
		{Stream: 1 << 40, Data: bytes.Repeat([]byte{0xff}, 1024), End: true},
		{Stream: 7, End: true}, // an end marker with no payload
	}

	for _, want := range cases {
		got, err := protocol.DecodeFrame(protocol.EncodeFrame(want))
		if err != nil {
			t.Fatalf("DecodeFrame: %v", err)
		}
		if got.Stream != want.Stream || got.End != want.End || !bytes.Equal(got.Data, want.Data) {
			t.Errorf("round trip changed the frame: got %+v, want %+v", got, want)
		}
	}
}

func TestDecodeFrameRejectsTruncatedInput(t *testing.T) {
	for _, size := range []int{0, 1, 8} {
		if _, err := protocol.DecodeFrame(make([]byte, size)); !errors.Is(err, protocol.ErrShortFrame) {
			t.Errorf("DecodeFrame of %d bytes returned %v, want ErrShortFrame", size, err)
		}
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	encoded, err := protocol.Encode(protocol.TypeRequest, protocol.Request{
		Stream: 3,
		Method: "GET",
		URI:    "/s/tok/video.mp4?raw=1",
		Header: map[string][]string{"Range": {"bytes=0-99"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := protocol.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Type != protocol.TypeRequest {
		t.Fatalf("Type = %q, want %q", envelope.Type, protocol.TypeRequest)
	}

	request, err := protocol.Payload[protocol.Request](envelope)
	if err != nil {
		t.Fatal(err)
	}
	// The Range header has to survive intact: it is what makes seeking work
	// through the relay.
	if request.Header["Range"][0] != "bytes=0-99" {
		t.Errorf("Range header = %v, want it preserved", request.Header["Range"])
	}
	if request.URI != "/s/tok/video.mp4?raw=1" {
		t.Errorf("URI = %q, want the query string preserved", request.URI)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := protocol.Decode([]byte("not json")); err == nil {
		t.Error("Decode accepted invalid JSON")
	}
	if _, err := protocol.Decode([]byte(`{"payload":{}}`)); err == nil {
		t.Error("Decode accepted an envelope with no type")
	}
}

func TestBodyStreamDeliversEverythingPushedBeforeClose(t *testing.T) {
	stream := protocol.NewBodyStream()

	go func() {
		for _, chunk := range []string{"hello ", "relay ", "world"} {
			if err := stream.Push([]byte(chunk)); err != nil {
				t.Errorf("Push: %v", err)
				return
			}
		}
		stream.Close(nil)
	}()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// Closing must not discard buffered data, or a complete response would be
	// truncated by its own end-of-stream frame.
	if string(got) != "hello relay world" {
		t.Errorf("read %q, want the full body", got)
	}
}

func TestBodyStreamReportsTruncation(t *testing.T) {
	stream := protocol.NewBodyStream()
	boom := errors.New("tunnel dropped")

	stream.Push([]byte("partial"))
	stream.Close(boom)

	data, err := io.ReadAll(stream)
	if string(data) != "partial" {
		t.Errorf("read %q, want the bytes that did arrive", data)
	}
	// A truncated transfer must be distinguishable from a complete one.
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the close reason", err)
	}
}

func TestBodyStreamCopiesCallerBuffers(t *testing.T) {
	stream := protocol.NewBodyStream()

	// The connection reuses one read buffer per frame, so the stream has to
	// copy; otherwise every chunk would alias the most recent frame.
	buf := []byte("first")
	stream.Push(buf)
	copy(buf, "XXXXX")
	stream.Close(nil)

	got, _ := io.ReadAll(stream)
	if string(got) != "first" {
		t.Errorf("read %q, want %q", got, "first")
	}
}

func TestBodyStreamPushAfterCloseDoesNotBlock(t *testing.T) {
	stream := protocol.NewBodyStream()
	stream.Close(nil)

	done := make(chan error, 1)
	go func() { done <- stream.Push([]byte("late")) }()

	select {
	case err := <-done:
		if !errors.Is(err, protocol.ErrStreamClosed) {
			t.Errorf("Push after Close returned %v, want ErrStreamClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Push blocked on a closed stream")
	}
}

func TestAgentPathIsNamespaced(t *testing.T) {
	// The agent endpoint must not be able to collide with a share route.
	if !strings.HasPrefix(protocol.AgentPath, "/_vrok/") {
		t.Errorf("AgentPath = %q, want it under the reserved /_vrok/ prefix", protocol.AgentPath)
	}
}
