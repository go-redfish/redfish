package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/go-authn/secretsource"
)

// A session token may live in a secret manager instead of the session file:
// `session login --session-store keychain:redfish/bmc1`. The file then holds
// the reference and no token, so the token never touches the disk.

// opened is a resolved secret: secretsource.Secret, or a test's fake.
type opened interface {
	Bytes() []byte
	Close() error
}

// The secret manager calls; tests replace them.
var (
	openSecretRef = func(ctx context.Context, ref string) (opened, error) {
		s, err := secretsource.Open(ctx, ref)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	storeSecretRef  = secretsource.Store
	deleteSecretRef = secretsource.Delete
)

// maxToken bounds a session token read back from a manager.
const maxToken = secretsource.MaxSize

// checkStoreRef accepts the references a session token can be kept under: a
// manager that Store writes and Delete removes. It never touches a manager.
func checkStoreRef(ref string) error {
	r, err := secretsource.Parse(ref)
	if err != nil {
		return fmt.Errorf("--session-store %s: %w", clean(ref), err)
	}
	switch {
	case r.Scheme == "keychain", r.Scheme == "keepassxc":
	case r.Scheme == "secretservice" && r.Attrs == "":
	default:
		return fmt.Errorf("--session-store %s: want keychain:SERVICE/ACCOUNT, secretservice:SERVICE/ACCOUNT or keepassxc:URL, which can be written and deleted", clean(ref))
	}
	return nil
}

// tokenOpenError is a token that could not be read back from its manager.
type tokenOpenError struct{ err error }

func (e *tokenOpenError) Error() string { return e.err.Error() }
func (e *tokenOpenError) Unwrap() error { return e.err }

// openToken reads the token kept under ref. The bytes are wiped when it
// returns, except the string the library needs.
func openToken(ctx context.Context, ref string) (string, error) {
	if err := checkStoreRef(ref); err != nil {
		return "", &tokenOpenError{err}
	}
	s, err := openSecretRef(ctx, ref)
	if err != nil {
		if errors.Is(err, secretsource.ErrTooLarge) {
			return "", &tokenOpenError{fmt.Errorf("session token %s: larger than %d bytes", clean(ref), maxToken)}
		}
		return "", &tokenOpenError{fmt.Errorf("session token %s: %w", clean(ref), err)}
	}
	defer s.Close()
	b := s.Bytes()
	if len(b) == 0 || len(b) > maxToken {
		return "", &tokenOpenError{fmt.Errorf("session token %s: %d bytes is not a token", clean(ref), len(b))}
	}
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			return "", &tokenOpenError{fmt.Errorf("session token %s: holds a control character", clean(ref))}
		}
	}
	return string(b), nil
}

// undoStore deletes the entry just stored (best effort) when the file could
// not be written, and returns err.
func undoStore(ctx context.Context, ref string, err error) error {
	if ref != "" {
		_ = deleteSecretRef(ctx, ref)
	}
	return err
}

// dropSession removes what a session left: the manager entry (when ref is
// set; already gone is fine), then the file. The file goes even when Delete
// fails; a Delete failure is returned after it.
func dropSession(ctx context.Context, path, ref string) error {
	var derr error
	if ref != "" {
		if err := deleteSecretRef(ctx, ref); err != nil && !errors.Is(err, fs.ErrNotExist) {
			derr = fmt.Errorf("the token entry %s was not deleted (delete it by hand): %w", clean(ref), err)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(err, derr)
	}
	return derr
}
