package twofactor

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/internal/storage"
)

// recoveryEncoding writes recovery codes: base32 without padding, so 10 random
// bytes make 16 characters.
var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// unused counts the recovery codes of subject that are not spent.
func (factors *Factors) unused(ctx context.Context, subject string) (int, error) {
	counts, err := factors.db.Select[int](ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.SelectRaw("COUNT(*)").Where("subject", subject).Where("used_at", 0)
	}))
	if err != nil {
		return 0, fmt.Errorf("twofactor: count recovery codes: %w", err)
	}

	return counts[0], nil
}

// replaceCodes deletes the recovery codes of subject and inserts a new set, in
// the caller's transaction, and returns the codes formatted for display.
func (factors *Factors) replaceCodes(ctx context.Context, subject string) ([]string, error) {
	_, err := factors.db.Exec(ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))
	if err != nil {
		return nil, fmt.Errorf("twofactor: clear recovery codes: %w", err)
	}
	display := make([]string, factors.count)
	rows := make([]map[string]any, factors.count)
	for slot := range factors.count {
		var raw [10]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, fmt.Errorf("twofactor: generate recovery code: %w", err)
		}
		plain := recoveryEncoding.EncodeToString(raw[:])
		display[slot] = plain[0:4] + "-" + plain[4:8] + "-" + plain[8:12] + "-" + plain[12:16]
		rows[slot] = map[string]any{"subject": subject, "slot": slot, "code_hash": storage.Hash(plain), "used_at": 0}
	}
	if _, err := factors.db.Exec(ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.InsertRows(rows...)
	})); err != nil {
		return nil, fmt.Errorf("twofactor: store recovery codes: %w", err)
	}

	return display, nil
}

// Recover spends a recovery code and returns the factor to record (method
// [MethodRecovery], kind possession). The case, dashes and spaces of the code
// do not matter. Every failure is [ErrInvalidCode]. Spending is one conditional
// update, so a code works once even under concurrency. It publishes
// [RecoveryUsed].
//
// Example:
//
//	factor, err := factors.Recover(ctx, subject, "ABCD-EFGH-IJKL-MNOP")
//	if errors.Is(err, twofactor.ErrInvalidCode) {
//		// wrong or spent code
//	}
//	_ = factor
func (factors *Factors) Recover(ctx context.Context, subject, code string) (auth.Factor, error) {
	normalized := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	affected, err := factors.db.Exec(ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.Where("subject", subject).Where("code_hash", storage.Hash(normalized)).Where("used_at", 0).
			Update(map[string]any{"used_at": time.Now().Unix()})
	}))
	if err != nil {
		return auth.Factor{}, fmt.Errorf("twofactor: spend recovery code: %w", err)
	}
	if affected == 0 {
		return auth.Factor{}, ErrInvalidCode
	}
	factors.events.Emit(ctx, RecoveryUsed, UserEvent{Subject: subject})

	return auth.Factor{Method: MethodRecovery, Kind: auth.KindPossession}, nil
}

// Regenerate replaces the user's recovery codes and returns the new ones,
// formatted; the old ones stop working at once. A user without an enabled
// authenticator gets [ErrNotEnabled]. It publishes [Regenerated].
//
// Example:
//
//	codes, err := factors.Regenerate(ctx, subject)
func (factors *Factors) Regenerate(ctx context.Context, subject string) ([]string, error) {
	var codes []string
	err := factors.db.WithTransaction(ctx, func(ctx context.Context) error {
		enabled, err := factors.Enrolled(ctx, subject)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrNotEnabled
		}
		codes, err = factors.replaceCodes(ctx, subject)

		return err
	})
	if err != nil {
		return nil, err
	}
	factors.afterCommit(ctx, func(ctx context.Context) {
		factors.events.Emit(ctx, Regenerated, UserEvent{Subject: subject})
	})

	return codes, nil
}
