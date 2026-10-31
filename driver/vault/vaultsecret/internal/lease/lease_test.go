package lease

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hashicorp/vault/api"
)

// fakeWatcher is a [Watcher] whose end the test triggers.
type fakeWatcher struct {
	// done is signaled by the test to end renewal.
	done chan error

	// stopped is closed by Stop.
	stopped chan struct{}

	// stopOnce makes Stop idempotent like the Vault watcher.
	stopOnce sync.Once
}

// newFakeWatcher returns a running-looking watcher.
func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{
		done:    make(chan error, 1),
		stopped: make(chan struct{}),
	}
}

// Start does nothing: the fake only reports completion through done.
func (*fakeWatcher) Start() {
}

// Stop records that the driver stopped renewal.
func (fake *fakeWatcher) Stop() {
	fake.stopOnce.Do(func() {
		close(fake.stopped)
	})
}

// DoneCh returns the channel the test signals.
func (fake *fakeWatcher) DoneCh() <-chan error {
	return fake.done
}

// renewableSecret is a login response with a renewable token.
func renewableSecret() *api.Secret {
	return &api.Secret{
		Auth: &api.SecretAuth{
			Renewable:     true,
			LeaseDuration: 60,
		},
	}
}

// fakeSession scripts logins and watchers for a driver under test.
type fakeSession struct {
	// mutex protects login counts, remaining failures, and watcher publication.
	mutex sync.Mutex

	// logins counts every authentication attempt, including failed retries.
	logins int

	// failures counts the remaining failed logins after the initial success.
	failures int

	// watchers retains each issued watcher in creation order for assertions.
	watchers []*fakeWatcher
}

// login fails the first failures calls after the initial one, then succeeds.
func (session *fakeSession) login(context.Context) (*api.Secret, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	session.logins++

	if session.logins > 1 && session.failures > 0 {
		session.failures--

		return nil, errors.New("vault down")
	}

	return renewableSecret(), nil
}

// newWatcher hands out a new fake watcher.
func (session *fakeSession) newWatcher(*api.Secret) (Watcher, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	fake := newFakeWatcher()
	session.watchers = append(session.watchers, fake)

	return fake, nil
}

// snapshot returns the login count and the watchers created so far.
func (session *fakeSession) snapshot() (int, []*fakeWatcher) {
	session.mutex.Lock()
	defer session.mutex.Unlock()

	return session.logins, append([]*fakeWatcher(nil), session.watchers...)
}

// newSessionDriver builds a driver wired to session with short retry delays.
func newSessionDriver(t *testing.T, session *fakeSession) *Manager {
	t.Helper()

	return New(session.login, session.newWatcher, slog.New(slog.DiscardHandler), time.Second, 4*time.Second)
}

// TestRenewalRelogsInWhenWatcherEnds verifies that when the watcher finishes,
// the driver logs in again and watches the new token.
func TestRenewalRelogsInWhenWatcherEnds(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		session := &fakeSession{}
		driver := newSessionDriver(t, session)

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		_, watchers := session.snapshot()
		watchers[0].done <- nil
		synctest.Wait()

		logins, watchers := session.snapshot()

		if logins != 2 || len(watchers) != 2 {
			t.Fatalf("logins = %d, watchers = %d, want 2 and 2", logins, len(watchers))
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestRenewalRetriesLoginWithBackoff verifies failed re-logins are retried
// with a doubling delay until one succeeds.
func TestRenewalRetriesLoginWithBackoff(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		session := &fakeSession{
			failures: 2,
		}
		driver := newSessionDriver(t, session)

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		_, watchers := session.snapshot()
		start := time.Now()
		watchers[0].done <- errors.New("renewal failed")
		time.Sleep(3 * time.Second)
		synctest.Wait()

		logins, watchers := session.snapshot()

		if logins != 4 || len(watchers) != 2 {
			t.Fatalf("logins = %d, watchers = %d, want 4 and 2", logins, len(watchers))
		}

		if elapsed := time.Since(start); elapsed != 3*time.Second {
			t.Fatalf("retries took %s, want 1s + 2s", elapsed)
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestStopEndsRenewalAndWatcher verifies Stop stops the watcher and waits for
// the goroutine.
func TestStopEndsRenewalAndWatcher(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		session := &fakeSession{}
		driver := newSessionDriver(t, session)

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}

		_, watchers := session.snapshot()

		select {
		case <-watchers[0].stopped:
		default:
			t.Fatal("watcher not stopped")
		}
	})
}

// TestStopInterruptsReloginBackoff verifies Stop does not wait for the retry
// delay of a failing re-login.
func TestStopInterruptsReloginBackoff(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		session := &fakeSession{
			failures: 100,
		}
		driver := newSessionDriver(t, session)

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		_, watchers := session.snapshot()
		watchers[0].done <- nil
		synctest.Wait()

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestStartSkipsRenewalForNonRenewableToken verifies no watcher runs when the
// login token cannot be renewed.
func TestStartSkipsRenewalForNonRenewableToken(t *testing.T) {
	t.Parallel()

	session := &fakeSession{}
	driver := newSessionDriver(t, session)
	driver.login = func(context.Context) (*api.Secret, error) {
		return &api.Secret{
			Auth: &api.SecretAuth{},
		}, nil
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, watchers := session.snapshot(); len(watchers) != 0 {
		t.Fatalf("watchers = %d, want 0", len(watchers))
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
