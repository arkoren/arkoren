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
// [Login.Logins] and [Login.LoginsOf]. It holds personal data (user agent and
// address): show it to its owner and administrators only, and never put it in
// telemetry or events.
//
// Example:
//
//	records, err := login.Logins(r)
//	if err != nil {
//		return err
//	}
//	for _, record := range records {
//		fmt.Println(record.UserAgent, record.IP, record.SeenAt, record.Current)
//	}
type LoginRecord struct {
	// ID identifies the login. It is random, stays the same when the session ID
	// rotates and is what [Login.Revoke] takes.
	ID string `json:"id"`

	// CreatedAt is when the user logged in, in UTC.
	CreatedAt time.Time `json:"created_at"`

	// SeenAt is when the login last authenticated a request, in UTC. It is
	// refreshed at most once per five minutes, so it can lag by that much.
	SeenAt time.Time `json:"seen_at"`

	// UserAgent is the User-Agent request header at login, truncated to 255
	// bytes. It is empty when [LoginBuilder.Devices] is false.
	UserAgent string `json:"user_agent,omitzero"`

	// IP is the client address at login, from [LoginBuilder.ClientIP]. It is empty
	// when [LoginBuilder.Devices] is false.
	IP string `json:"ip,omitzero"`

	// Current is true for the record of the request's own login. Only
	// [Login.Logins] sets it; it is never stored.
	Current bool `json:"-"`
}

// remoteHost is the default [LoginBuilder.ClientIP]: the host of r.RemoteAddr.
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
// against a lock that expired mid-edit and against [Login.RevokeAll]. A list
// with no records is not created. change receives a list it may modify and
// return; it may run several times.
func (login *Login) edit(ctx context.Context, subject string, change func([]LoginRecord) []LoginRecord) error {
	key := login.records.Key(subject)

	lease, err := login.store.Lock(ctx, "arkoren:auth:logins:lock:"+subject, lockTTL)

	if err != nil {
		return err
	}

	defer func() { _ = lease.Unlock(context.WithoutCancel(ctx)) }()

	for range maxEditAttempts {
		raw, err := login.store.Driver().Get(ctx, key.Name())

		if errors.Is(err, cache.ErrNotFound) {
			next := change(nil)

			if len(next) == 0 {
				return nil
			}

			added, err := login.store.Add(ctx, key, next)

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

		applied, err := login.store.CompareAndPut(ctx, cache.Match{Key: key.Name(), Value: raw}, key, change(current))

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
// oldest records beyond [LoginBuilder.MaxLogins] are dropped, which logs those
// devices out.
func (login *Login) open(r *http.Request, subject string) (string, error) {
	now := time.Now().UTC()
	record := LoginRecord{ID: rand.Text(), CreatedAt: now, SeenAt: now}

	if login.settings.Devices {
		record.UserAgent = deviceField(r.UserAgent())
		record.IP = deviceField(login.settings.ClientIP(r))
	}

	err := login.edit(r.Context(), subject, func(current []LoginRecord) []LoginRecord {
		current = append(current, record)

		return current[max(0, len(current)-login.settings.MaxLogins):]
	})

	return record.ID, err
}

// list returns the stored records of subject, oldest first; none is a nil list.
func (login *Login) list(ctx context.Context, subject string) ([]LoginRecord, error) {
	current, err := login.store.Get(ctx, login.records.Key(subject))

	if errors.Is(err, cache.ErrNotFound) {
		return nil, nil
	}

	return current, err
}

// seen reports whether the record id of subject still exists and refreshes its
// SeenAt when it is older than five minutes. The common case is one read.
func (login *Login) seen(ctx context.Context, subject, id string) (bool, error) {
	current, err := login.list(ctx, subject)

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

	err = login.edit(ctx, subject, func(current []LoginRecord) []LoginRecord {
		for i := range current {
			if current[i].ID == id && time.Since(current[i].SeenAt) >= seenInterval {
				current[i].SeenAt = time.Now().UTC()
			}
		}

		return current
	})

	return true, err
}

// Logins returns the login records of the request's user, oldest first, with
// [LoginRecord.Current] set on the record of this request's own login. It
// returns [ErrUnauthenticated] when the session holds no logged-in user and
// [session.ErrNoSession] without a session. Mount it behind [Require].
//
// Example:
//
//	records, err := login.Logins(r)
//	if err != nil {
//		return err
//	}
//	return response.JSON(w, http.StatusOK, records)
func (login *Login) Logins(r *http.Request) ([]LoginRecord, error) {
	current, err := login.session(r)

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

	records, err := login.LoginsOf(r.Context(), stored.Subject)

	if err != nil {
		return nil, err
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
//	records, err := login.LoginsOf(ctx, subject)
//	if err != nil {
//		return err
//	}
//	fmt.Println(len(records), "devices")
func (login *Login) LoginsOf(ctx context.Context, subject string) ([]LoginRecord, error) {
	if err := ValidateSubject(subject); err != nil {
		return nil, err
	}

	return login.list(ctx, subject)
}

// Revoke logs the devices with the given record IDs out: each device's next
// request is anonymous, because the authenticator no longer finds its record.
// Unknown IDs are ignored, so it is idempotent. It returns an error wrapping
// [ErrInvalidSubject] for an invalid subject, and cache or lock errors; after an
// error nothing is guaranteed to have been revoked, so retry.
//
// Example:
//
//	if err := login.Revoke(r.Context(), subject, r.PathValue("id")); err != nil {
//		return err
//	}
func (login *Login) Revoke(ctx context.Context, subject string, ids ...string) error {
	if err := ValidateSubject(subject); err != nil {
		return err
	}

	return login.edit(ctx, subject, func(current []LoginRecord) []LoginRecord {
		return slices.DeleteFunc(current, func(record LoginRecord) bool { return slices.Contains(ids, record.ID) })
	})
}

// RevokeOthers logs every device of the request's user out except the one that
// made the request. It returns [ErrUnauthenticated] when the session holds no
// logged-in user and [session.ErrNoSession] without a session.
//
// Example:
//
//	if err := login.RevokeOthers(r); err != nil {
//		return err
//	}
func (login *Login) RevokeOthers(r *http.Request) error {
	current, err := login.session(r)

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

	return login.edit(r.Context(), stored.Subject, func(records []LoginRecord) []LoginRecord {
		return slices.DeleteFunc(records, func(record LoginRecord) bool { return record.ID != stored.LoginID })
	})
}

// RevokeAll logs every device of subject out by deleting the subject's record
// list. It returns an error wrapping [ErrInvalidSubject] for an invalid subject.
// [NewLogin] registers it with [UserProvider.OnForget], so forgetting a user
// ends their sessions.
//
// Example:
//
//	if err := login.RevokeAll(ctx, subject); err != nil {
//		return err
//	}
func (login *Login) RevokeAll(ctx context.Context, subject string) error {
	if err := ValidateSubject(subject); err != nil {
		return err
	}

	return login.store.Delete(ctx, login.records.Key(subject))
}
