package storage_test

import (
	"context"
	"fmt"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
)

// TestPutAndDeletePublishEvents checks successful writes and deletions emit redacted metadata.
func TestPutAndDeletePublishEvents(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var written []storage.ObjectWrittenEvent
	var deleted []storage.ObjectDeletedEvent

	if _, err := bus.Listen(storage.ObjectWritten, func(_ context.Context, payload storage.ObjectWrittenEvent) error {
		written = append(written, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(storage.ObjectDeleted, func(_ context.Context, payload storage.ObjectDeletedEvent) error {
		deleted = append(deleted, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := storage.New(memorystorage.New(), func(store *storage.Builder) {
		store.Name("uploads")
	}, func(store *storage.Builder) {
		store.Events(bus)
	})

	if _, err := store.PutBytes(t.Context(), "avatars/1.png", []byte("png")); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(t.Context(), "avatars/1.png"); err != nil {
		t.Fatal(err)
	}

	if len(written) != 1 || written[0].Store != "uploads" || written[0].Object.Key != "avatars/1.png" || written[0].Object.Size != 3 {
		t.Fatalf("written = %+v", written)
	}

	if len(deleted) != 1 || deleted[0].Store != "uploads" || deleted[0].Key != "avatars/1.png" {
		t.Fatalf("deleted = %+v", deleted)
	}
}

// TestInvalidPutPublishesNothing verifies input rejection does not emit a write event.
func TestInvalidPutPublishesNothing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	published := false

	if _, err := bus.Listen(storage.ObjectWritten, func(context.Context, storage.ObjectWrittenEvent) error {
		published = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := storage.New(memorystorage.New(), func(store *storage.Builder) {
		store.Events(bus)
	})

	if _, err := store.PutBytes(t.Context(), "../escape", []byte("x")); err == nil {
		t.Fatal("put accepted an invalid key")
	}

	if published {
		t.Fatal("ObjectWritten published for a failed put")
	}
}

// ExampleObjectWritten executes the declaration-local listener pattern with an
// ordinary construction alias and a synchronous memory write.
func ExampleObjectWritten() {
	bus := event.New()

	unsubscribe, err := bus.Listen(storage.ObjectWritten, func(ctx context.Context, payload storage.ObjectWrittenEvent) error {
		fmt.Println(payload.Store, payload.Object.Size)

		return nil
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	defer unsubscribe()
	policies := []storage.BuilderFunc{
		func(store *storage.Builder) {
			store.Events(bus)
			store.Name("reports")
		},
	}

	files := storage.New(memorystorage.New(), policies...)

	if _, err := files.PutBytes(context.Background(), "report.txt", []byte("report")); err != nil {
		fmt.Println(err)
	}
	// Output: reports 6
}
