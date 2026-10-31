// Package redistest provides an in-process RESP peer for native-client deadline
// tests. It never dials Redis. Run delayed-response tests inside synctest.Test so
// the peer's waits use virtual time, and stop clients before leaving the bubble.
//
// Example:
//
//	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
//	redistest.DelayPing(t, client)
//	t.Cleanup(func() {
//		if err := client.Close(); err != nil {
//			t.Error(err)
//		}
//	})
//	if err := client.Ping(t.Context()).Err(); err != nil {
//		t.Fatal(err)
//	}
package redistest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// pipeHook replaces only connection establishment with an in-memory socket.
// Command encoding, initialization, deadlines, and replies stay SDK-owned.
type pipeHook struct {
	// connection is the client end, closed by the owning driver during cleanup.
	connection net.Conn
}

// DialHook supplies the local socket without consulting the configured address.
// Tests attach the hook before making any native-client calls.
func (hook pipeHook) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) {
		return hook.connection, nil
	}
}

// ProcessHook leaves ordinary native command execution unchanged. Only dialing
// is substituted, so a test observes real go-redis timeout behavior.
func (pipeHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}

// ProcessPipelineHook leaves initialization pipelines on the native path. The
// RESP peer accepts their CLIENT commands just as it accepts ordinary commands.
func (pipeHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// DelayPing attaches a RESP2 peer that answers the first PING immediately and
// delays later PING replies for two seconds. No server, address, or SDK options
// are changed. Cleanup closes both socket ends and joins the serving goroutine;
// the caller must stop its client before that cleanup runs.
//
// Example:
//
//	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
//	redistest.DelayPing(t, client)
//	t.Cleanup(func() {
//		if err := client.Close(); err != nil {
//			t.Error(err)
//		}
//	})
func DelayPing(t *testing.T, client *redis.Client) {
	t.Helper()

	connection, peer := net.Pipe()
	client.AddHook(pipeHook{connection: connection})

	done := make(chan error, 1)

	go func() {
		done <- servePing(peer)
	}()

	t.Cleanup(func() {
		_ = connection.Close() // Unblock either side even after a failed assertion.
		_ = peer.Close()       // The fixture owns both in-process socket ends.

		if err := <-done; err != nil {
			t.Errorf("RESP peer: %v", err)
		}
	})
}

// servePing declines HELLO, accepts CLIENT initialization, and delays only after
// the first successful PING. Expected peer closure ends the fixture normally.
func servePing(peer net.Conn) error {
	reader := bufio.NewReader(peer)
	ready := false

	for {
		command, err := readCommand(reader)

		if err != nil {
			return closedPeer(err)
		}

		reply := "+OK\r\n"

		switch command[0] {
		case "hello":
			reply = "-ERR unknown command 'hello'\r\n"
		case "ping":
			if ready {
				time.Sleep(2 * time.Second)
			}

			ready = true
			reply = "+PONG\r\n"
		}

		if _, err := io.WriteString(peer, reply); err != nil {
			return closedPeer(err)
		}
	}
}

// readCommand decodes native RESP arrays without inspecting application values
// or Lua source. Invalid framing and short reads are fixture failures.
func readCommand(reader *bufio.Reader) ([]string, error) {
	var length int

	if _, err := fmt.Fscanf(reader, "*%d\r\n", &length); err != nil {
		return nil, err
	}

	command := make([]string, length)

	for index := range command {
		if _, err := fmt.Fscanf(reader, "$%d\r\n", &length); err != nil {
			return nil, err
		}

		payload := make([]byte, length+2)

		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, err
		}

		command[index] = string(payload[:length])
	}

	return command, nil
}

// closedPeer distinguishes intentional socket teardown from malformed RESP or
// unexpected I/O failures, which must remain visible to the test owner.
func closedPeer(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return nil // Cancellation can interrupt a partially encoded request.
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}
