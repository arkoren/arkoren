// Package protocoltest drives the public HTTP service-protocol seam of a Host
// without a Restate server. It sends version-5 start/input frames and acknowledges
// actual Run proposals; it does not emulate retries, timers, or engine durability.
// Tests inspect observable output and state commands, not driver internals.
//
//	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
//		Path: "/invoke/Demo/Run",
//		Input: []byte(`{"v":"input"}`),
//	})
//	_ = frames
package protocoltest

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"arkoren.dev/driver/restate"
)

// Invocation supplies one native invocation, not an ingress request. State is
// an eager snapshot retained only while encoding the request. Invoke copies it
// onto the wire; nil state means the object has no stored keys.
//
//	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
//		Path: "/invoke/Demo/Run", Key: "run-42", Input: []byte(`null`),
//	})
//	_ = frames
type Invocation struct {
	// Path is the endpoint's native invocation path, including /invoke.
	//
	//	_ = Invocation{Path: "/invoke/Demo/Run"}
	Path string

	// Key is the logical object/workflow key; empty is valid for a service.
	//
	//	_ = Invocation{Key: "run-42"}
	Key string

	// Input is valid JSON consumed by the registered native handler.
	//
	//	_ = Invocation{Input: []byte(`{"v":"input"}`)}
	Input []byte

	// State maps native state keys to their encoded JSON values.
	//
	//	_ = Invocation{State: map[string][]byte{"gen": []byte(`3`)}}
	State map[string][]byte

	// RetryCount models retries since the last stored journal entry, not a
	// completed retry loop.
	//
	//	_ = Invocation{RetryCount: 1}
	RetryCount uint64

	// journal contains commands and acknowledged notifications captured by
	// Replay. Ordinary invocations have only the initial input journal entry.
	journal []Frame
}

// Frame contains one response message as specified by service protocol v5.
// Bytes and Uint decode individual fields without depending on SDK internals.
//
//	for _, frame := range protocoltest.Invoke(t, host, invocation) {
//		_ = frame.Bytes(14)
//	}
type Frame struct {
	// Type is the numeric service-protocol message type, not an HTTP status.
	//
	//	output := frame.Type == 0x0401
	//	_ = output
	Type uint16

	// Body is the protobuf payload, owned independently of the HTTP reader.
	//
	//	_ = Frame{Body: data}.Bytes(1)
	Body []byte
}

// Bytes returns a length-delimited field or nil when absent or malformed. It
// returns a slice of Body; callers must not mutate frames they still inspect.
//
//	value := frame.Bytes(14)
//	_ = protocoltest.Frame{Body: value}.Bytes(1)
func (frame Frame) Bytes(number int) []byte {
	value, _ := protowire.ConsumeBytes(field(frame.Body, protowire.Number(number)))

	return value
}

// Uint returns a varint field and whether it was explicitly encoded. This
// distinguishes an optional zero retry delay from an omitted delay. Ordinary
// proto3 scalar fields can omit their valid zero default, so presence alone is
// not a validity check for every numeric field.
//
//	delay, present := frame.Uint(8)
//	_ = delay
//	_ = present
func (frame Frame) Uint(number int) (uint64, bool) {
	value, size := protowire.ConsumeVarint(field(frame.Body, protowire.Number(number)))

	return value, size >= 0
}

// field locates the encoded value of a protobuf field. Invalid wire data is
// treated as absent, causing the caller's semantic assertion to fail.
func field(body []byte, number protowire.Number) []byte {
	for len(body) > 0 {
		current, kind, size := protowire.ConsumeTag(body)

		if size < 0 {
			return nil
		}

		body = body[size:]
		size = protowire.ConsumeFieldValue(current, kind, body)

		if size < 0 {
			return nil
		}

		if current == number {
			return body[:size]
		}

		body = body[size:]
	}

	return nil
}

// bytesField appends a length-delimited protobuf field, including empty values.
// Presence matters for protocol oneofs such as successful empty Run output.
func bytesField(body []byte, number protowire.Number, value []byte) []byte {
	body = protowire.AppendTag(body, number, protowire.BytesType)

	return protowire.AppendBytes(body, value)
}

// uintField appends a varint protobuf field. Numeric protocol identifiers and
// counts retain their native unsigned representation.
func uintField(body []byte, number protowire.Number, value uint64) []byte {
	body = protowire.AppendTag(body, number, protowire.VarintType)

	return protowire.AppendVarint(body, value)
}

// wire prefixes a protobuf message with the protocol's eight-byte frame header.
// Flags are zero because these test invocations require no command acknowledgments.
func wire(kind uint16, body []byte) []byte {
	header := make([]byte, 8)
	binary.BigEndian.PutUint16(header, kind)
	binary.BigEndian.PutUint32(header[4:], uint32(len(body)))

	return append(header, body...)
}

// Bytes builds independent start and input frames for a native HTTP request.
// One known entry means only input is replayed; Run closures execute in the host.
//
//	body := bytes.NewReader(protocoltest.Invocation{Input: []byte(`null`)}.Bytes())
//	_ = body
func (invocation Invocation) Bytes() []byte {
	start := bytesField(nil, 1, []byte("0123456789abcdef"))
	start = bytesField(start, 2, []byte("inv_protocol_test"))
	start = uintField(start, 3, uint64(1+len(invocation.journal)))
	start = bytesField(start, 6, []byte(invocation.Key))
	start = uintField(start, 7, invocation.RetryCount)

	for key, value := range invocation.State {
		entry := bytesField(nil, 1, []byte(key))
		entry = bytesField(entry, 2, value)
		start = bytesField(start, 4, entry)
	}

	input := bytesField(nil, 14, bytesField(nil, 1, invocation.Input))
	body := append(wire(0, start), wire(0x0400, input)...)

	for _, frame := range invocation.journal {
		body = append(body, wire(frame.Type, frame.Body)...)
	}

	return body
}

// Replay resubmits the same invocation with the commands and acknowledged Run
// results from a previous Invoke. The real SDK decides which closures to skip;
// this helper neither invokes application handlers nor supplies invented results.
// This checks SDK replay, not persistence or routing by a real Restate server.
//
//	first := protocoltest.Invoke(t, host, invocation)
//	replayed := protocoltest.Replay(t, host, invocation, first)
//	_ = replayed
func Replay(t *testing.T, host *restate.Host, invocation Invocation, frames []Frame) []Frame {
	t.Helper()

	for _, frame := range frames {
		switch {
		case frame.Type == 5:
			invocation.journal = append(invocation.journal, completion(frame))
		case frame.Type >= 0x0400:
			invocation.journal = append(invocation.journal, frame)
		}
	}

	return Invoke(t, host, invocation)
}

// Start starts a host after its public drivers/runners have registered handlers.
// Cleanup stops the endpoint and joins its serving goroutine before returning.
// Configure a loopback Addr, AllowInsecure, and no Register to keep the endpoint
// local. This helper does not provision an external Restate server.
//
//	host := restate.New(func(host *restate.Builder) {
//		host.Addr("127.0.0.1:0")
//		host.AllowInsecure(true)
//	})
//	protocoltest.Start(t, host)
func Start(t *testing.T, host *restate.Host) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	if err := host.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- host.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := host.Stop(ctx); err != nil {
			t.Error(err)
		}

		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

// Invoke sends an invocation through a started Host and returns all response
// frames through End, Error, or suspension. HTTP/2 supports full-duplex Run
// acknowledgments. A bounded context and joined initial writer prevent leaks
// when the handler or protocol is broken. No external service is contacted.
//
//	frames := protocoltest.Invoke(t, host, protocoltest.Invocation{
//		Path: "/invoke/Demo/Run", Input: []byte(`{"v":"input"}`),
//	})
//	_ = frames
func Invoke(t *testing.T, host *restate.Host, invocation Invocation) []Frame {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	reader, writer := io.Pipe()
	var writing sync.WaitGroup
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
		writing.Wait()
	})

	initialized := make(chan error, 1)
	writing.Go(func() {
		_, err := writer.Write(invocation.Bytes())
		initialized <- err
	})

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host.Addr().String()+invocation.Path, reader)

	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Content-Type", "application/vnd.restate.invocation.v5")

	transport := &http.Transport{Protocols: new(http.Protocols)}
	transport.Protocols.SetUnencryptedHTTP2(true)
	t.Cleanup(transport.CloseIdleConnections)

	response, err := (&http.Client{Transport: transport}).Do(request)

	if err != nil {
		_ = reader.Close()
		<-initialized
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	if err := <-initialized; err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("invocation HTTP status = %d", response.StatusCode)
	}

	return readFrames(t, response.Body, writer)
}

// readFrames acknowledges actual Run results and stops at a terminal protocol
// frame. It does not manufacture successful handler results or retry outcomes.
func readFrames(t *testing.T, reader io.Reader, writer *io.PipeWriter) []Frame {
	t.Helper()

	var frames []Frame

	for {
		header := make([]byte, 8)

		if _, err := io.ReadFull(reader, header); err != nil {
			t.Fatalf("read protocol header after %d frames: %v", len(frames), err)
		}

		frame := Frame{Type: binary.BigEndian.Uint16(header), Body: make([]byte, binary.BigEndian.Uint32(header[4:]))}

		if _, err := io.ReadFull(reader, frame.Body); err != nil {
			t.Fatal(err)
		}

		frames = append(frames, frame)

		if frame.Type <= 3 {
			break
		}

		if frame.Type == 5 {
			acknowledge(t, writer, frame)
		}
	}

	_ = writer.Close()

	// Join the server-side request lifetime rather than closing an HTTP/2
	// stream while the SDK is still draining request input.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}

	return frames
}

// acknowledge reflects a Run proposal as a stored completion. Protocol v5 uses
// raw proposal bytes, but wraps completion values in a Value message.
func acknowledge(t *testing.T, writer io.Writer, proposal Frame) {
	t.Helper()

	frame := completion(proposal)

	if _, err := writer.Write(wire(frame.Type, frame.Body)); err != nil {
		t.Fatal(err)
	}
}

// completion converts an actual Run proposal into its stored notification.
// Live acknowledgments and replay journals must encode the same result bytes.
func completion(proposal Frame) Frame {
	completionID, _ := proposal.Uint(1)
	body := uintField(nil, 1, completionID)

	if value := field(proposal.Body, 14); value != nil {
		body = bytesField(body, 5, bytesField(nil, 1, proposal.Bytes(14)))
	} else {
		body = bytesField(body, 6, proposal.Bytes(15))
	}

	return Frame{Type: 0x8011, Body: body}
}
