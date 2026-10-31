package router

import (
	"testing"

	"arkoren.dev/core/event"
)

// TestRecoverSettingsReplaceHandler verifies callback order.
func TestRecoverSettingsReplaceHandler(t *testing.T) {
	t.Parallel()

	config := newRecoverSettings(
		func(recovery *RecoverBuilder) {
			recovery.settings = recoverSettings{
				Handler: func(any) error {
					return nil
				},
			}
		},
		func(recovery *RecoverBuilder) {
			recovery.Handler(nil)
		},
	)

	if config.Handler != nil {
		t.Fatal("handler not overridden")
	}
}

// TestRecoverBuilderPublicationDetachesRoot checks completed settings do not retain the
// mutable construction root. Services and function captures remain shared.
func TestRecoverBuilderPublicationDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *RecoverBuilder
	config := newRecoverSettings(func(recovery *RecoverBuilder) {
		retained = recovery
		recovery.Events(nil)
	})
	retained.Events(event.New())

	if config.Events != nil {
		t.Fatal("retained draft changed published settings")
	}
}
