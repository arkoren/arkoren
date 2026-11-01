//go:build windows

package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// certificatePrivateDescriptor builds a protected DACL granting only the current
// user's SID full access. Setting it at creation prevents other users from
// retaining a readable handle to an initially unprotected empty private file.
func certificatePrivateDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()

	if err != nil {
		return nil, fmt.Errorf("read current Windows user: %w", err)
	}

	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")

	if err != nil {
		return nil, fmt.Errorf("construct private file ACL: %w", err)
	}

	return descriptor, nil
}

// Create applies private protection atomically with exclusive
// creation. NtCreateFile accepts the pinned parent handle and a security
// descriptor together; os.Root.OpenFile cannot supply the latter. FILE_CREATE
// never opens an existing leaf, including a link. Public files use os.Root.
// Application key output and scaffold .env reuse this primitive as well as
// certificate private keys; none temporarily inherits the parent directory DACL.
func Create(destination Destination) (*os.File, error) {
	name := filepath.Base(destination.Path)

	if destination.Mode != 0o600 {
		return destination.Parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, destination.Mode)
	}

	descriptor, err := certificatePrivateDescriptor()

	if err != nil {
		return nil, err
	}

	parent, err := destination.Parent.Open(".")

	if err != nil {
		return nil, err
	}

	defer parent.Close() // Read-only directory handle has no buffered output.

	objectName, err := windows.NewNTUnicodeString(name)

	if err != nil {
		return nil, err
	}

	attributes := &windows.OBJECT_ATTRIBUTES{
		RootDirectory:      windows.Handle(parent.Fd()),
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var handle windows.Handle

	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_WRITE, attributes, &windows.IO_STATUS_BLOCK{},
		nil, windows.FILE_ATTRIBUTE_NORMAL, 0, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)

	if status, ok := errors.AsType[windows.NTStatus](err); ok {
		err = status.Errno()
	}

	if err != nil {
		return nil, &os.PathError{Op: "create private file", Path: destination.Path, Err: err}
	}

	return os.NewFile(uintptr(handle), destination.Path), nil
}
