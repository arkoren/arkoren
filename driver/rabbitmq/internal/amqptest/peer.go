// Package amqptest supplies a small scripted AMQP 0-9-1 peer for driver tests.
// It uses real sockets and the public SDK, not driver-internal injection seams.
// Scripts choose when to acknowledge methods, return publications, or stall.
//
// Example:
//
//	url := amqptest.New(t, func(peer *amqptest.Peer) {
//		peer.Handshake()
//	})
//	_ = url
package amqptest

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Peer is one test-owned socket. Its script runs on a single goroutine; cleanup
// closes the socket and joins the script even when the driver fails early.
//
// Example:
//
//	amqptest.New(t, func(peer *amqptest.Peer) {
//		peer.Handshake()
//	})
type Peer struct {
	// connection carries frames; closing it interrupts a stalled script.
	connection net.Conn
}

// Frame retains the native channel and payload without interpreting content
// headers. Method identifies method frames, and is zero for other frame types.
//
// Example:
//
//	frame := peer.Read()
//	if frame.Method == 0x003c0028 {
//		peer.Send(1, frame.Channel, []byte{0, 60, 0, 80, 0, 0, 0, 0, 0, 0, 0, 1, 0})
//	}
type Frame struct {
	// Type is the AMQP frame type (1 method, 2 content header, 3 body).
	// Example: if frame.Type == 3 { body := frame.Payload; _ = body }
	Type byte
	// Channel is the broker channel identifier; replies must preserve it.
	// Example: peer.Send(1, frame.Channel, []byte{0, 20, 0, 41})
	Channel uint16
	// Payload is owned by this frame and may be edited by the script.
	// Example: body := string(frame.Payload); _ = body
	Payload []byte
	// Method combines the class and method identifiers for method frames.
	// Example: if frame.Method == 0x00140028 { /* channel.close */ }
	Method uint32
}

// New starts a single-connection loopback listener and returns its AMQP URL.
// The test owns both ends; no RabbitMQ process or shared port is contacted.
// A script panic reports an error unless cleanup already closed its socket.
//
// Example:
//
//	url := amqptest.New(t, func(peer *amqptest.Peer) {
//		peer.Handshake()
//	})
//	_ = url
func New(t *testing.T, script func(*Peer)) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	var sockets sync.Map
	t.Cleanup(func() {
		_ = listener.Close() // Cleanup interrupts Accept if the client never dialed.

		sockets.Range(func(connection, _ any) bool {
			_ = connection.(net.Conn).Close() // Cleanup interrupts scripted reads.

			return true
		})

		group.Wait()
	})
	group.Go(func() {
		connection, err := listener.Accept()

		if err != nil {
			return // Cleanup closed a listener whose client never connected.
		}

		sockets.Store(connection, true)
		defer connection.Close()
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Logf("AMQP peer ended: %v", recovered)
			}
		}()

		_ = connection.SetDeadline(time.Now().Add(5 * time.Second)) // Bounds a broken test script.
		script(&Peer{connection: connection})
	})

	return "amqp://guest:guest@" + listener.Addr().String() + "/"
}

// Handshake completes the connection-level AMQP negotiation with heartbeats
// disabled. It panics on protocol or socket failure to terminate the script.
//
// Example:
//
//	peer.Handshake()
//	frame := peer.Read()
//	_ = frame
func (peer *Peer) Handshake() {
	header := make([]byte, 8)

	if _, err := io.ReadFull(peer.connection, header); err != nil {
		panic(err)
	}

	peer.Send(1, 0, []byte{0, 10, 0, 10, 0, 9, 0, 0, 0, 0, 0, 0, 0, 5, 'P', 'L', 'A', 'I', 'N', 0, 0, 0, 5, 'e', 'n', '_', 'U', 'S'})
	peer.Read() // connection.start-ok
	peer.Send(1, 0, []byte{0, 10, 0, 30, 0, 0, 0, 2, 0, 0, 0, 0})
	peer.Read() // connection.tune-ok
	peer.Read() // connection.open
	peer.Send(1, 0, []byte{0, 10, 0, 41, 0})
}

// Read receives one complete frame. Socket errors panic so a script cannot
// accidentally continue after an incomplete read.
//
// Example:
//
//	frame := peer.Read()
//	_ = frame.Method
func (peer *Peer) Read() Frame {
	header := make([]byte, 7)

	if _, err := io.ReadFull(peer.connection, header); err != nil {
		panic(err)
	}

	payload := make([]byte, int(binary.BigEndian.Uint32(header[3:]))+1)

	if _, err := io.ReadFull(peer.connection, payload); err != nil {
		panic(err)
	}

	frame := Frame{Type: header[0], Channel: binary.BigEndian.Uint16(header[1:]), Payload: payload[:len(payload)-1]}

	if frame.Type == 1 {
		frame.Method = binary.BigEndian.Uint32(frame.Payload)
	}

	return frame
}

// Send emits a native frame. The caller supplies the complete method or content
// payload, without the frame header and terminator.
//
// Example:
//
//	peer.Send(1, 1, []byte{0, 20, 0, 11, 0, 0, 0, 0}) // channel.open-ok
func (peer *Peer) Send(kind byte, channel uint16, payload []byte) {
	frame := make([]byte, 7, len(payload)+8)
	frame[0] = kind
	binary.BigEndian.PutUint16(frame[1:], channel)
	binary.BigEndian.PutUint32(frame[3:], uint32(len(payload)))
	frame = append(frame, payload...)
	frame = append(frame, 0xce)

	if _, err := peer.connection.Write(frame); err != nil {
		panic(err)
	}
}

// Setup acknowledges the topology and channel methods used by both drivers.
// It returns the first method it does not recognize so scripts can inspect or
// stall publications, probes, and shutdown. No content is acknowledged here.
//
// Example:
//
//	peer.Handshake()
//	publication := peer.Setup()
//	_ = publication
func (peer *Peer) Setup() Frame {
	for {
		frame := peer.Read()

		if !peer.ReplySetup(frame) {
			return frame
		}
	}
}

// ReplySetup acknowledges a recognized channel or topology method and reports
// whether it replied. Scripts can stop at a chosen RPC to test cancellation.
//
// Example:
//
//	frame := peer.Read()
//	if !peer.ReplySetup(frame) {
//		t.Errorf("unexpected setup method: %08x", frame.Method)
//	}
func (peer *Peer) ReplySetup(frame Frame) bool {
	replies := map[uint32][]byte{
		0x0014000a: {0, 20, 0, 11, 0, 0, 0, 0}, // channel.open
		0x0028000a: {0, 40, 0, 11},             // exchange.declare
		0x0032000a: {0, 50, 0, 11, 1, 'q', 0, 0, 0, 0, 0, 0, 0, 0},
		0x00320014: {0, 50, 0, 21}, // queue.bind
		0x003c000a: {0, 60, 0, 11}, // basic.qos
		0x0055000a: {0, 85, 0, 11}, // confirm.select
	}

	reply, found := replies[frame.Method]

	if found {
		peer.Send(1, frame.Channel, reply)
	}

	return found
}

// Deliver accepts basic.consume and sends one unsettled delivery using its tag.
// The body is a complete portable envelope; the test controls its trusted
// routing key and delivery tag independently of the embedded JSON values.
//
// Example:
//
//	frame := peer.Setup()
//	peer.Deliver(frame, []byte(`{"name":"work","payload":"e30="}`))
func (peer *Peer) Deliver(consume Frame, body []byte) {
	offset := 7 + int(consume.Payload[6])
	tag := consume.Payload[offset : offset+1+int(consume.Payload[offset])]
	peer.Send(1, consume.Channel, append([]byte{0, 60, 0, 21}, tag...))

	deliver := append([]byte{0, 60, 0, 60}, tag...)
	deliver = append(deliver, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 4, 'w', 'o', 'r', 'k')
	peer.Send(1, consume.Channel, deliver)

	header := make([]byte, 14)
	binary.BigEndian.PutUint16(header, 60)
	binary.BigEndian.PutUint64(header[4:], uint64(len(body)))
	peer.Send(2, consume.Channel, header)
	peer.Send(3, consume.Channel, body)
}

// ReturnPublication reads a publication's header and body, returns them with
// NO_ROUTE, and positively confirms the supplied publish sequence number.
// The contradictory outcomes intentionally model real unroutable AMQP work.
//
// Example:
//
//	frame := peer.Setup()
//	peer.ReturnPublication(frame, 1)
func (peer *Peer) ReturnPublication(publish Frame, sequence uint64) {
	header := peer.Read()
	body := peer.Read()
	peer.Send(1, publish.Channel, []byte{0, 60, 0, 50, 1, 56, 8, 'N', 'O', '_', 'R', 'O', 'U', 'T', 'E', 0, 0})
	peer.Send(2, publish.Channel, header.Payload)
	peer.Send(3, publish.Channel, body.Payload)

	ack := make([]byte, 13)
	binary.BigEndian.PutUint16(ack, 60)
	binary.BigEndian.PutUint16(ack[2:], 80)
	binary.BigEndian.PutUint64(ack[4:], sequence)
	peer.Send(1, publish.Channel, ack)
}
