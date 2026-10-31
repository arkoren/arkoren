package session

import "context"

// Handle selects a request session independently of cookies and storage. Zero
// selects the default; copies select the same bridge. It does not isolate
// storage, cookies, or authenticated users (which remain keyed by user type).
//
// Example:
//
//	admin := session.NewHandle()
//	ctx := session.NewContextFor(context.Background(), admin, current)
//	_, _ = session.FromContextFor(ctx, admin)
type Handle struct {
	// token has nonzero size to guarantee distinct live allocation addresses.
	token *byte
}

// sessionKey prevents raw context.WithValue(handle, value) from populating the
// official session bridge. Only NewContextFor constructs this private key.
type sessionKey struct {
	// handle selects the bridge, including the zero default.
	handle Handle
}

// NewHandle creates a unique comparable selector without I/O. Share its value
// between middleware, identity definitions and request lookup.
//
// Example:
//
//	admin := session.NewHandle()
//	r.Use(session.Middleware(store, func(cookies *session.Builder) {
//		cookies.Handle(admin)
//	}))
func NewHandle() Handle {
	return Handle{token: new(byte)}
}

// NewContext stores the default session. Nil hides an earlier default value and
// is reported as absent by FromContext; no session is created here.
//
// Example:
//
//	ctx := session.NewContext(context.Background(), current)
//	_, _ = session.FromContext(ctx)
func NewContext(ctx context.Context, value *Session) context.Context {
	return NewContextFor(ctx, Handle{}, value)
}

// FromContext reads the default session without creating one. A typed nil
// pointer is absent; custom handle selections do not affect this lookup.
//
// Example:
//
//	current, ok := session.FromContext(ctx)
//	if !ok {
//		return session.ErrNoSession
//	}
//	_ = current
func FromContext(ctx context.Context) (*Session, bool) {
	return FromContextFor(ctx, Handle{})
}

// NewContextFor stores value under handle without changing other selections.
// Zero selects default; nil hides an earlier value for this handle. A raw
// context.WithValue call keyed by handle does not populate this bridge.
//
// Example:
//
//	ctx := session.NewContextFor(context.Background(), admin, current)
//	_ = ctx
func NewContextFor(ctx context.Context, handle Handle, value *Session) context.Context {
	return context.WithValue(ctx, sessionKey{handle: handle}, value)
}

// FromContextFor reads exactly handle once, without fallback or mutation. Zero
// reads default; a missing value or typed nil pointer returns nil, false.
//
// Example:
//
//	current, ok := session.FromContextFor(ctx, admin)
//	if !ok {
//		return session.ErrNoSession
//	}
//	_ = current
func FromContextFor(ctx context.Context, handle Handle) (*Session, bool) {
	value, ok := ctx.Value(sessionKey{handle: handle}).(*Session)

	return value, ok && value != nil
}
