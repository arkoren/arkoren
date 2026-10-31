package storage

import (
	"cmp"

	"arkoren.dev/core/event"
)

// ObjectWrittenEvent reports metadata after a successful facade write. It
// contains no contents or signed URL; events are observations, not durable work.
//
// Example:
//
//	unsubscribe, err := bus.Listen(storage.ObjectWritten, func(ctx context.Context, payload storage.ObjectWrittenEvent) error {
//		fmt.Println(payload.Store, payload.Object.Size)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type ObjectWrittenEvent struct {
	// Store is [Builder.Name], or [DefaultName] when it is empty.
	//
	// Example:
	//
	//	payload := storage.ObjectWrittenEvent{Store: storage.DefaultName}
	//	fmt.Println(payload.Store) // default
	Store string

	// Object describes the stored object.
	//
	// Example:
	//
	//	payload := storage.ObjectWrittenEvent{Object: storage.ObjectInfo{Key: "report.txt", Size: 6}}
	//	fmt.Println(payload.Object.Size) // 6
	Object ObjectInfo
}

// ObjectDeletedEvent reports successful idempotent deletion through the facade.
// It is also emitted for an already absent object; it does not prove a prior
// object existed and is not a durable delivery receipt.
//
// Example:
//
//	unsubscribe, err := bus.Listen(storage.ObjectDeleted, func(ctx context.Context, payload storage.ObjectDeletedEvent) error {
//		fmt.Println(payload.Store)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type ObjectDeletedEvent struct {
	// Store is [Builder.Name], or [DefaultName] when it is empty.
	//
	// Example:
	//
	//	payload := storage.ObjectDeletedEvent{Store: storage.DefaultName}
	//	fmt.Println(payload.Store) // default
	Store string

	// Key is the deleted key.
	//
	// Example:
	//
	//	payload := storage.ObjectDeletedEvent{Key: "obsolete/report.txt"}
	//	fmt.Println(payload.Key) // obsolete/report.txt
	Key string
}

var (
	// ObjectWritten is published when [Storage.Put], or a helper built on it,
	// stores an object.
	//
	// Example:
	//
	//	unsubscribe, err := bus.Listen(storage.ObjectWritten, func(ctx context.Context, payload storage.ObjectWrittenEvent) error {
	//		fmt.Println(payload.Object.Size)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	ObjectWritten = event.NewTopic[ObjectWrittenEvent]("storage.object.written")

	// ObjectDeleted is published when [Storage.Delete] succeeds.
	// It reports the API observation even when the key was absent already.
	//
	// Example:
	//
	//	unsubscribe, err := bus.Listen(storage.ObjectDeleted, func(ctx context.Context, payload storage.ObjectDeletedEvent) error {
	//		fmt.Println(payload.Store)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe()
	ObjectDeleted = event.NewTopic[ObjectDeletedEvent]("storage.object.deleted")
)

// name returns the store name events and telemetry report.
func (storage *Storage) name() string {
	return cmp.Or(storage.config.Name, DefaultName)
}
