package auth

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/service/cache"
)

// Login-record limits.
const (
	// seenInterval is the longest a record's SeenAt may lag behind the latest
	// authenticated request. It keeps ordinary requests read-only.
	seenInterval = 5 * time.Minute

	// maxDeviceField is the longest user agent or address kept, in bytes.
	maxDeviceField = 255

	// lockTTL bounds how long a crashed writer can block a subject's records.
	lockTTL = 5 * time.Second

	// maxEditAttempts bounds the compare-and-put retries of one edit.
	maxEditAttempts = 16
)

// errRecordsBusy reports an edit that lost the compare-and-put race on every
// attempt, even while holding the subject's lock.
var errRecordsBusy = errors.New("auth: login records are too busy to update")

// LoginRecord is one login of one user on one device, as listed by
// [Guard.Logins] and [Guard.LoginsOf]. It holds personal data (user agent and
// address): show it to its owner and administrators only, and never put it in
// telemetry or events.
//
// Example:
//
//	records, err := guard.Logins(r)
//	if err != nil {
//		return err
//	}
//	for _, record := range records {
//		fmt.Println(record.UserAgent, record.IP, record.SeenAt, record.Current)
//	}
type LoginRecord struct {
	// ID identifies the login. It is random, stays the same when the session ID
	// rotates and is what [Guard.Revoke] takes.
	ID string `json:"id"`

	// CreatedAt is when the user logged in, in UTC.
	CreatedAt time.Time `json:"created_at"`

	// SeenAt is when the login last authenticated a request, in UTC. It is
	// refreshed at most once per five minutes and on a best-effort basis, so it
	// can lag by that much or more.
	SeenAt time.Time `json:"seen_at"`

	// UserAgent is the User-Agent request header at login, truncated to 255
	// bytes. It is empty when [GuardBuilder.Devices] is false.
	UserAgent string `json:"user_agent,omitzero"`

	// IP is the client address at login, from [GuardBuilder.ClientIP], cut to 255
	// bytes. It is empty when [GuardBuilder.Devices] is false.
	IP string `json:"ip,omitzero"`

	// Current is true for the record of the request's own login. Only
	// [Guard.Logins] sets it; it is never stored, so it appears in JSON only
	// when true.
	Current bool `json:"current,omitzero"`
}

// remoteHost is the default [GuardBuilder.ClientIP]: the host of r.RemoteAddr.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)

	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// deviceField returns value cut to at most 255 bytes and valid UTF-8, which the
// cache's JSON encoding requires.
func deviceField(value string) string {
	return strings.ToValidUTF8(value[:min(len(value), maxDeviceField)], "")
}

// edit applies change to the subject's list of records and stores the result.
// It holds the cache lock of the subject so that concurrent logins queue instead
// of racing, and still compares and puts the exact bytes it read, which guards
// against a lock that expired mid-edit and against [Guard.RevokeAll]. A list
// with no records is not created. change receives only the live records (see
// live), so every write also drops the dead ones; it may modify and return the
// list and may run several times.
func (guard *Guard) edit(ctx context.Context, subject string, change func([]LoginRecord) []LoginRecord) error {
	key := guard.records.Key(subject)

	lease, err := guard.store.Lock(ctx, "arkoren:auth:logins:lock:"+subject, lockTTL)

	if err != nil {
		return err
	}

	// The edit is already stored or failed; a failed unlock only delays the next
	// writer until the lease expires, so its error cannot change the outcome.
	defer func() { _ = lease.Unlock(context.WithoutCancel(ctx)) }()

	for range maxEditAttempts {
		raw, err := guard.store.Driver().Get(ctx, key.Name())

		if errors.Is(err, cache.ErrNotFound) {
			next := change(nil)

			if len(next) == 0 {
				return nil
			}

			added, err := guard.store.Add(ctx, key, next)

			if err != nil {
				return err
			}

			if added {
				return nil
			}

			continue
		}

		if err != nil {
			return err
		}

		var current []LoginRecord

		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}

		applied, err := guard.store.CompareAndPut(ctx, cache.Match{Key: key.Name(), Value: raw}, key, change(guard.live(current)))

		if err != nil {
			return err
		}

		if applied {
			return nil
		}
	}

	return errRecordsBusy
}

// open adds a record for a login of subject made by r and returns its ID. The
// oldest records beyond [GuardBuilder.MaxLogins] are dropped, which logs those
// devices out.
func (guard *Guard) open(r *http.Request, subject string) (string, error) {
	now := time.Now().UTC()
	record := LoginRecord{ID: rand.Text(), CreatedAt: now, SeenAt: now}

	if guard.settings.Devices {
		record.UserAgent = deviceField(r.UserAgent())
		record.IP = deviceField(guard.settings.ClientIP(r))
	}

	err := guard.edit(r.Context(), subject, func(current []LoginRecord) []LoginRecord {
		current = append(current, record)

		return current[max(0, len(current)-guard.settings.MaxLogins):]
	})

	return record.ID, err
}

// live removes the records created at least [GuardBuilder.SessionLifetime] ago
// from records and returns the rest. Sessions have an absolute lifetime, so such
// a record cannot belong to a live session: its device is already logged out and
// listing it would show a phantom device. It modifies records in place, so pass
// a freshly decoded list.
func (guard *Guard) live(records []LoginRecord) []LoginRecord {
	return slices.DeleteFunc(records, func(record LoginRecord) bool {
		return time.Since(record.CreatedAt) >= guard.settings.SessionLifetime
	})
}

// list returns the stored records of subject that are still live, oldest first;
// none is a nil list.
func (guard *Guard) list(ctx context.Context, subject string) ([]LoginRecord, error) {
	current, err := guard.store.Get(ctx, guard.records.Key(subject))

	if errors.Is(err, cache.ErrNotFound) {
		return nil, nil
	}

	return guard.live(current), err
}

// seen reports whether the record id of subject still exists and refreshes its
// SeenAt when it is older than five minutes. The common case is one read. Only
// the read can fail; see the refresh below.
func (guard *Guard) seen(ctx context.Context, subject, id string) (bool, error) {
	current, err := guard.list(ctx, subject)

	if err != nil {
		return false, err
	}

	index := slices.IndexFunc(current, func(record LoginRecord) bool { return record.ID == id })

	if index < 0 {
		return false, nil
	}

	if time.Since(current[index].SeenAt) < seenInterval {
		return true, nil
	}

	// The refresh is best effort: the record exists, so a failed write (lock
	// contention, cache error) only leaves SeenAt stale and must not fail a
	// request that is otherwise authenticated. The next request retries.
	_ = guard.edit(ctx, subject, func(current []LoginRecord) []LoginRecord {
		for i := range current {
			if current[i].ID == id && time.Since(current[i].SeenAt) >= seenInterval {
				current[i].SeenAt = time.Now().UTC()
			}
		}

		return current
	})

	return true, nil
}

// Logins returns the login records of the request's user, oldest first, with
// [LoginRecord.Current] set on the record of this request's own login. It
// returns [ErrUnauthenticated] when the session holds no logged-in user or its
// own login was revoked, and [session.ErrNoSession] without a session. Mount it
// behind [Require].
//
// Example:
//
//	records, err := guard.Logins(r)
//	if err != nil {
//		return err
//	}
//	return response.JSON(w, http.StatusOK, records)
func (guard *Guard) Logins(r *http.Request) ([]LoginRecord, error) {
	current, err := guard.session(r)

	if err != nil {
		return nil, err
	}

	stored, err := current.Get(sessionField)

	if missing(err) {
		return nil, ErrUnauthenticated
	}

	if err != nil {
		return nil, err
	}

	records, err := guard.LoginsOf(r.Context(), stored.Subject)

	if err != nil {
		return nil, err
	}

	// A revoked device must not read the user's other devices.
	if !slices.ContainsFunc(records, func(record LoginRecord) bool { return record.ID == stored.LoginID }) {
		return nil, ErrUnauthenticated
	}

	for i := range records {
		records[i].Current = records[i].ID == stored.LoginID
	}

	return records, nil
}

// LoginsOf returns the login records of subject, oldest first, for
// administration. It returns an error wrapping [ErrInvalidSubject] for an invalid
// subject and no records, not an error, for a user without logins. The records
// are personal data.
//
// Example:
//
//	records, err := guard.LoginsOf(ctx, subject)
//	if err != nil {
//		return err
//	}
//	fmt.Println(len(records), "devices")
func (guard *Guard) LoginsOf(ctx context.Context, subject string) ([]LoginRecord, error) {
	if err := ValidateSubject(subject); err != nil {
		return nil, err
	}

	return guard.list(ctx, subject)
}

// Revoke logs the devices with the given record IDs out: each device's next
// request is anonymous, because the authenticator no longer finds its record.
// Unknown IDs are ignored, so it is idempotent. It returns an error wrapping
// [ErrInvalidSubject] for an invalid subject, and cache or lock errors; after an
// error nothing is guaranteed to have been revoked, so retry.
//
// Example:
//
//	if err := guard.Revoke(r.Context(), subject, r.PathValue("id")); err != nil {
//		return err
//	}
func (guard *Guard) Revoke(ctx context.Context, subject string, ids ...string) error {
	if err := ValidateSubject(subject); err != nil {
		return err
	}

	return guard.edit(ctx, subject, func(current []LoginRecord) []LoginRecord {
		return slices.DeleteFunc(current, func(record LoginRecord) bool { return slices.Contains(ids, record.ID) })
	})
}

// RevokeOthers logs every device of the request's user out except the one that
// made the request. It returns [ErrUnauthenticated] when the session holds no
// logged-in user or its own login was revoked (nothing is revoked then), and
// [session.ErrNoSession] without a session.
//
// Example:
//
//	if err := guard.RevokeOthers(r); err != nil {
//		return err
//	}
func (guard *Guard) RevokeOthers(r *http.Request) error {
	current, err := guard.session(r)

	if err != nil {
		return err
	}

	stored, err := current.Get(sessionField)

	if missing(err) {
		return ErrUnauthenticated
	}

	if err != nil {
		return err
	}

	own := func(record LoginRecord) bool { return record.ID == stored.LoginID }

	records, err := guard.list(r.Context(), stored.Subject)

	if err != nil {
		return err
	}

	if !slices.ContainsFunc(records, own) {
		return ErrUnauthenticated
	}

	return guard.edit(r.Context(), stored.Subject, func(records []LoginRecord) []LoginRecord {
		// A revocation between the check and the edit must not wipe the list.
		if !slices.ContainsFunc(records, own) {
			return records
		}

		return slices.DeleteFunc(records, func(record LoginRecord) bool { return !own(record) })
	})
}

// RevokeAll logs every device of subject out by deleting the subject's record
// list. It returns an error wrapping [ErrInvalidSubject] for an invalid subject.
// [NewGuard] registers it with [UserProvider.OnForget], so forgetting a user
// ends their sessions.
//
// Example:
//
//	if err := guard.RevokeAll(ctx, subject); err != nil {
//		return err
//	}
func (guard *Guard) RevokeAll(ctx context.Context, subject string) error {
	if err := ValidateSubject(subject); err != nil {
		return err
	}

	return guard.store.Delete(ctx, guard.records.Key(subject))
}
