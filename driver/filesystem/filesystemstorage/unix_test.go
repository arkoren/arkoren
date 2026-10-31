//go:build unix

package filesystemstorage_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"arkoren.dev/driver/filesystem/filesystemstorage"
)

// TestStatRejectsNamedPipe observes a special entry created at the filesystem
// boundary. It must not be advertised as a readable regular storage object.
func TestStatRejectsNamedPipe(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	driver, err := filesystemstorage.New(root)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if _, err := driver.Stat(t.Context(), "pipe"); err == nil {
		t.Fatal("Stat accepted a named pipe as an object")
	}
}

// TestOpenRejectsNamedPipeWithoutWriter keeps unsupported FIFO entries from
// waiting for an external writer. The timer only bounds a regression failure;
// cleanup supplies a writer and joins Open so even the failing version exits.
func TestOpenRejectsNamedPipeWithoutWriter(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pipe := filepath.Join(root, "pipe")

	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}

	driver, err := filesystemstorage.New(root)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	done := make(chan error, 1)

	go func() {
		object, err := driver.Open(t.Context(), "pipe")

		if err == nil {
			err = object.Contents.Close()
		}

		done <- err
		close(done)
	}()
	t.Cleanup(func() {
		writer, err := os.OpenFile(pipe, os.O_RDWR|syscall.O_NONBLOCK, 0)

		if err != nil {
			t.Error(err)

			return
		}

		<-done

		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Open accepted a named pipe as an object")
		}
	case <-time.After(time.Second):
		t.Fatal("Open blocked waiting for a FIFO writer")
	}
}
