// Package awsstorage provides an Amazon S3 storage driver. Its Open and Stat methods
// wrap [storage.ErrNotFound] when a key is absent.
// Constructors accept ordered Builder callbacks invoked directly once on one
// fresh draft. Nil entries panic; expanded nil/empty slices omit edits. Completed
// settings are copied; native clients and providers are not cloned. NewFrom
// applies only Prefix and neither copies nor closes its native client.
// PUT signing accepts a content type without a length. If native middleware or
// an injected presigner omits or changes that requested signed type, SignURL
// returns [storage.ErrUnsupported] rather than an unbound upload URL. Positive
// lengths bind an exact size; zero leaves size unbound. See [Driver.SignURL].
// List filters nonportable native keys and may read ahead to validate logical
// continuation without changing native cursor strings. Opened bodies belong to
// the caller. Already-canceled calls perform no I/O; arbitrary readers/native
// dependencies must cooperate with cancellation once an operation has started.
//
// Example:
//
//	driver, err := awsstorage.New(ctx, "uploads", func(store *awsstorage.Builder) {
//		store.Prefix("tenant-a")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
package awsstorage
