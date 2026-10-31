// Package amqpconn contains the RabbitMQ transport's context-aware boundaries.
// It keeps cancellation active through TLS and AMQP negotiation, where the
// SDK's custom Dial hook otherwise only cancels the TCP connection attempt.
//
// Example:
//
//	connection, abort, err := amqpconn.Dial(ctx, "amqp://localhost/")
//	if err != nil {
//		return err
//	}
//	return amqpconn.Close(ctx, connection, abort)
package amqpconn

import (
	"context"
	"net"
	"sync"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// handshakeTimeout bounds TCP, TLS, and AMQP negotiation together, including
// calls whose parent context does not carry a deadline.
const handshakeTimeout = 30 * time.Second

// closeTimeout bounds cleanup when an application supplies no earlier deadline.
// It applies to owned sockets; borrowed SDK RPCs cannot be forcibly interrupted.
const closeTimeout = 5 * time.Second

// Dial opens a non-recovering SDK connection. Cancellation closes the pending
// socket, and no cancellation callback remains attached after success.
//
// Example:
//
//	connection, abort, err := amqpconn.Dial(ctx, "amqp://localhost/")
//	if err != nil {
//		return err
//	}
//	_ = connection
//	return abort()
func Dial(ctx context.Context, url string) (*amqp091.Connection, func() error, error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	stop := func() {}
	var abort func() error

	connection, err := amqp091.DialConfig(url, amqp091.Config{
		Dial: func(network, address string) (net.Conn, error) {
			socket, err := (&net.Dialer{}).DialContext(ctx, network, address)

			if err != nil {
				return nil, err
			}

			abort = socket.Close
			done := make(chan struct{})
			cancelSocket := context.AfterFunc(ctx, func() {
				defer close(done)

				_ = socket.Close() // Cancellation intentionally abandons negotiation.
			})
			stop = func() {
				if !cancelSocket() {
					<-done
				}
			}

			return socket, nil
		},
	})

	stop()

	if ctx.Err() != nil {
		err = ctx.Err()
	}

	if err != nil {
		if abort != nil {
			_ = abort() // Release partial setup, including cancellation racing successful negotiation.
		}

		return nil, nil, err
	}

	return connection, abort, err
}

// Interrupt closes an owned connection if ctx ends during a blocking SDK RPC.
// The returned function must run before returning to the caller; it joins any
// in-flight cancellation callback. Never use this on a borrowed connection.
//
// Example:
//
//	finish := amqpconn.Interrupt(ctx, abort)
//	defer finish()
//	channel, err := connection.Channel()
//	_ = channel
//	return err
func Interrupt(ctx context.Context, abort func() error) func() {
	if abort == nil {
		return func() {} // Borrowed connections must remain caller-owned.
	}

	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)

		_ = abort() // Cancellation intentionally abandons the owned socket.
	})

	return func() {
		if !stop() {
			<-done
		}
	}
}

// Run executes a blocking SDK operation, interrupting an owned socket when ctx
// ends and preserving context error identity. A nil abort retains borrowed
// ownership: the SDK RPC itself then requires a responsive peer or caller close.
//
// Example:
//
//	return amqpconn.Run(ctx, abort, func() error {
//		channel, err := connection.Channel()
//		_ = channel
//		return err
//	})
func Run(ctx context.Context, abort func() error, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	finish := Interrupt(ctx, abort)
	defer finish()

	err := operation()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	return err
}

// Close releases an owned connection. It bounds a graceful close to five
// seconds when ctx has no earlier deadline, and cancellation closes the socket
// directly so even an already-expired context releases every native channel.
//
// Example:
//
//	return amqpconn.Close(ctx, connection, abort)
func Close(ctx context.Context, connection *amqp091.Connection, abort func() error) error {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()

	finish := Interrupt(ctx, abort)
	defer finish()

	deadline, _ := ctx.Deadline()
	err := connection.CloseDeadline(deadline)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	return err
}

// CloseChannel drains unread deliveries while closing a native consumer
// channel. An owned socket is interrupted after at most five seconds or the
// earlier ctx deadline. Nil abort preserves borrowed ownership and cannot bound
// an unresponsive SDK RPC. Callers normally detach an already-canceled consume
// context to allow a responsive peer to close just this channel.
//
// Example:
//
//	return amqpconn.CloseChannel(context.WithoutCancel(ctx), abort, channel, deliveries)
func CloseChannel(ctx context.Context, abort func() error, channel *amqp091.Channel, deliveries <-chan amqp091.Delivery) error {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()

	finish := Interrupt(ctx, abort)
	defer finish()

	var group sync.WaitGroup
	group.Go(func() {
		for range deliveries {
		}
	})

	err := channel.Close()
	group.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	return err
}
