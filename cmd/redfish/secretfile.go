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
	st, err := statOpen(f)
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

// statOpen is the Stat of the descriptor, a variable so that a test can make
// it fail.
var statOpen = (*os.File).Stat

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
	_, _ = rand.Read(rnd[:]) // never fails: it ends the program if the system has no randomness
	tmp := p + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	// 0600 is a ceiling the umask can only lower, so the file is never wider.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	if err := fillSecret(f, b); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// fillSecret writes b to the temporary file and closes it, whatever the
// write returned. A variable so that a test can make the disk fail.
var fillSecret = func(f *os.File, b []byte) error {
	_, werr := f.Write(b)
	return errors.Join(werr, f.Close())
}
