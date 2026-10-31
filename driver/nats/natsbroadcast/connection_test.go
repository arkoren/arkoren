package natsbroadcast_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/driver/nats/natsbroadcast"

	"github.com/nats-io/nkeys"
)

// TestNKeySeedSignsNativeNonce verifies the CONNECT authentication fields at the
// native wire seam, including the signature, without connecting a shared server.
func TestNKeySeedSignsNativeNonce(t *testing.T) {
	t.Parallel()

	pair, err := nkeys.FromRawSeed(nkeys.PrefixByteUser, bytes.Repeat([]byte{1}, 32))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pair.Wipe)
	seed, err := pair.Seed()

	if err != nil {
		t.Fatal(err)
	}

	endpoint := protocolServer(t, `{"server_id":"test","proto":1,"auth_required":true,"nonce":"challenge","max_payload":1048576}`, func(conn net.Conn, line string) {
		if line == "PING\r\n" {
			_, _ = fmt.Fprint(conn, "PONG\r\n")
		}

		if !strings.HasPrefix(line, "CONNECT ") {
			return
		}

		var packet struct {
			// Signature is the native URL-safe nonce signature.
			Signature string `json:"sig"`
		}

		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "CONNECT ")), &packet); err != nil {
			t.Error(err)

			return
		}

		signature, err := base64.RawURLEncoding.DecodeString(packet.Signature)

		if err != nil {
			t.Error(err)

			return
		}

		if err := pair.Verify([]byte("challenge"), signature); err != nil {
			t.Error("CONNECT nonce signature is invalid")
		}
	})
	driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.URLs(endpoint)
		broker.NKeySeed(string(seed))
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestSchemelessURLPasswordPreservesNativeAuthentication formats the public
// record before passing the same URL slice to New. Its CONNECT frame must still
// carry the original user/password, not diagnostic placeholders or normalized
// copies. The private loopback peer never contacts shared infrastructure.
func TestSchemelessURLPasswordPreservesNativeAuthentication(t *testing.T) {
	t.Parallel()

	// info requires authentication without a nonce. The callback checks the
	// client's basic credentials instead of substituting an SDK option.
	const info = `{"server_id":"test","proto":1,"auth_required":true,"max_payload":1048576}`
	endpoint := protocolServer(t, info, func(conn net.Conn, line string) {
		if line == "PING\r\n" {
			_, _ = fmt.Fprint(conn, "PONG\r\n")
		}

		if !strings.HasPrefix(line, "CONNECT ") {
			return
		}

		var packet struct {
			// Username is the native URL username supplied by the caller.
			Username string `json:"user"`
			// Password is the native URL password, never the redacted diagnostic.
			Password string `json:"pass"`
		}

		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "CONNECT ")), &packet); err != nil {
			t.Error(err)

			return
		}

		if packet.Username != "wire-private-user" || packet.Password != "wire-private-password" {
			t.Error("diagnostic formatting changed native URL authentication")
		}
	})
	config := natsbroadcast.Config{URLs: []string{
		"wire-private-user:wire-private-password@" + strings.TrimPrefix(endpoint, "nats://"),
	}}

	for _, output := range configDiagnostics(t, config) {
		if strings.Contains(output, "wire-private") {
			t.Fatal("schemeless credential exposed before connection")
		}
	}

	driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.URLs(config.URLs...)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestSchemelessURLTokenPreservesNativeAuthentication checks the passwordless
// userinfo form used by NATS tokens. Rendering Config cannot consume or rewrite
// the token later sent through the native CONNECT protocol.
func TestSchemelessURLTokenPreservesNativeAuthentication(t *testing.T) {
	t.Parallel()

	// info requests authentication without a nonce so the callback can inspect
	// the original passwordless URL token in the native CONNECT frame.
	const info = `{"server_id":"test","proto":1,"auth_required":true,"max_payload":1048576}`
	endpoint := protocolServer(t, info, func(conn net.Conn, line string) {
		if line == "PING\r\n" {
			_, _ = fmt.Fprint(conn, "PONG\r\n")
		}

		if !strings.HasPrefix(line, "CONNECT ") {
			return
		}

		var packet struct {
			// Token is the native passwordless userinfo credential.
			Token string `json:"auth_token"`
		}

		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "CONNECT ")), &packet); err != nil {
			t.Error(err)

			return
		}

		if packet.Token != "wire-private-token" {
			t.Error("diagnostic formatting changed native URL token")
		}
	})
	config := natsbroadcast.Config{URLs: []string{
		"wire-private-token@" + strings.TrimPrefix(endpoint, "nats://"),
	}}

	for _, output := range configDiagnostics(t, config) {
		if strings.Contains(output, "wire-private") {
			t.Fatal("schemeless token exposed before connection")
		}
	}

	driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.URLs(config.URLs...)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestStopWaitsForNativeDrainFlush holds the drain PONG at the native seam.
// Stop must neither return nor close the connection until the flush completes.
func TestStopWaitsForNativeDrainFlush(t *testing.T) {
	t.Parallel()

	draining := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			close(release)
		})
	})
	pings := 0
	endpoint := protocolServer(t, `{"server_id":"test","proto":1,"max_payload":1048576}`, func(conn net.Conn, line string) {
		if line != "PING\r\n" {
			return
		}

		pings++

		if pings == 2 {
			close(draining)
			<-release
		}

		_, _ = fmt.Fprint(conn, "PONG\r\n")
	})
	driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.URLs(endpoint)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		result <- driver.Stop(t.Context())
	})
	t.Cleanup(func() {
		once.Do(func() {
			close(release)
		})
		group.Wait()
	})

	select {
	case <-draining:
	case err := <-result:
		t.Fatalf("Stop returned before native drain flush: %v", err)
	}

	if driver.Connection().IsClosed() {
		t.Fatal("Stop aborted pending drain")
	}

	once.Do(func() {
		close(release)
	})

	if err := <-result; err != nil {
		t.Fatal(err)
	}

	if !driver.Connection().IsClosed() {
		t.Fatal("Stop returned before connection closed")
	}
}

// protocolServer supplies a single private NATS handshake/flush peer. The test
// controls native responses rather than replacing driver-private functions;
// cleanup closes the socket and joins its worker.
func protocolServer(t *testing.T, info string, receive func(net.Conn, string)) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	var mutex sync.Mutex
	var connection net.Conn
	var group sync.WaitGroup
	group.Go(func() {
		conn, err := listener.Accept()

		if err != nil {
			return
		}

		mutex.Lock()
		connection = conn
		mutex.Unlock()
		defer conn.Close()

		_, _ = fmt.Fprintf(conn, "INFO %s\r\n", info)

		reader := bufio.NewReader(conn)

		for {
			line, err := reader.ReadString('\n')

			if err != nil {
				return
			}

			receive(conn, line)
		}
	})
	t.Cleanup(func() {
		_ = listener.Close()
		mutex.Lock()

		if connection != nil {
			_ = connection.Close()
		}

		mutex.Unlock()
		group.Wait()
	})

	return "nats://" + listener.Addr().String()
}
