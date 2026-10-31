package natsbroadcast_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/driver/nats/natsbroadcast"
)

// natsTestURL is the address of the NATS server started by the compose file in the driver directory.
const natsTestURL = "nats://127.0.0.1:4222"

// TestConformance runs the broadcast conformance suite against a NATS server.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
		id := make([]byte, 6)

		if _, err := rand.Read(id); err != nil {
			t.Fatal(err)
		}

		driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
			broker.URLs(natsTestURL)
			broker.Prefix("conformance" + hex.EncodeToString(id))
		})

		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		if err := driver.Start(ctx); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_ = driver.Stop(context.Background())
		})

		return broadcasttest.Driver{
			Publisher: driver,
			Consumer:  driver,
		}
	})
}
