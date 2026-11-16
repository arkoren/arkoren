package apitoken

import (
	"context"
	"fmt"

	"arkoren.dev/core/event"
	"arkoren.dev/foundation/query"
)

// Revoke deletes the listed tokens of subject; ids that are unknown or belong
// to another user are ignored. A revoked token fails to authenticate at once.
// It publishes [Revoked] when at least one token was deleted.
//
// Example:
//
//	err := issuer.Revoke(ctx, subject, token.ID)
func (issuer *Issuer) Revoke(ctx context.Context, subject string, ids ...string) error {
	_, err := issuer.revoke(ctx, subject, ids)

	return err
}

// revoke deletes the tokens and reports how many were deleted.
func (issuer *Issuer) revoke(ctx context.Context, subject string, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	affected, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("subject", subject)
		statement.WhereIn("id", ids...)
		statement.Delete()
	}))
	if err != nil {
		return 0, fmt.Errorf("apitoken: revoke: %w", err)
	}
	if affected > 0 {
		issuer.emit(ctx, Revoked, subject)
	}

	return affected, nil
}

// RevokeAll deletes every token of subject ("sign out everywhere"). It is also
// the forget hook of the user provider. It publishes [Revoked] when at least one
// token was deleted.
//
// Example:
//
//	err := issuer.RevokeAll(ctx, subject)
func (issuer *Issuer) RevokeAll(ctx context.Context, subject string) error {
	affected, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))
	if err != nil {
		return fmt.Errorf("apitoken: revoke all: %w", err)
	}
	if affected > 0 {
		issuer.emit(ctx, Revoked, subject)
	}

	return nil
}

// RevokeDevices deletes the device tokens of subject and keeps their personal
// access tokens. A device holds no session that a password change could end,
// so the password handlers call it to make the change reach the devices; the
// personal tokens are the user's own deliberate credentials and survive. It
// publishes [Revoked] when at least one token was deleted.
//
// Example:
//
//	err := issuer.RevokeDevices(ctx, subject)
func (issuer *Issuer) RevokeDevices(ctx context.Context, subject string) error {
	affected, err := issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("subject", subject).Where("kind", string(KindDevice)).Delete()
	}))
	if err != nil {
		return fmt.Errorf("apitoken: revoke devices: %w", err)
	}
	if affected > 0 {
		issuer.emit(ctx, Revoked, subject)
	}

	return nil
}

// emit publishes topic with the subject once the surrounding transaction, if
// any, has committed.
func (issuer *Issuer) emit(ctx context.Context, topic event.Topic[UserEvent], subject string) {
	// A failing hook only loses a notification, never the committed change.
	_ = issuer.db.AfterCommit(ctx, func(ctx context.Context) error {
		issuer.events.Emit(ctx, topic, UserEvent{Subject: subject})

		return nil
	})
}
