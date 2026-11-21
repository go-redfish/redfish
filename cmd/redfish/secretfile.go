package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// openSecret opens a secret file ONCE, without following a symlink, and checks
// the descriptor it will read: a regular file, not readable by group or others,
// not larger than max. Checking a path and then opening it again would read a
// file that is not the one checked.
func openSecret(path, what string, max int64) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow|oNonBlock, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	switch {
	case !st.Mode().IsRegular():
		f.Close()
		return nil, fmt.Errorf("%s %s is not a regular file", what, path)
	case st.Mode().Perm()&0o077 != 0:
		f.Close()
		return nil, fmt.Errorf("%s %s is readable by group or others (mode %04o): %s", what, path, st.Mode().Perm(), unsafeModeHint(what))
	case st.Size() > max:
		f.Close()
		return nil, fmt.Errorf("%s %s is larger than %d bytes", what, path, max)
	}
	return f, nil
}

func unsafeModeHint(what string) string {
	if what == "session file" {
		return "remove it and log in again"
	}
	return "chmod 600 it"
}

// readSecret reads at most max bytes, and refuses a file that grew past it
// between the Stat and the read.
func readSecret(f *os.File, what, path string, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s %s is larger than %d bytes", what, path, max)
	}
	return b, nil
}

// writeSecret writes a file that only its owner can read, replacing any. The
// data goes to a temporary file with a random name, created exclusively and
// without following a symlink, in the same directory; the rename then replaces
// the destination itself (a symlink there is replaced, not followed).
func writeSecret(p string, b []byte) error {
	var rnd [12]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := p + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(b); err != nil {
		return fail(err)
	}
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
