//go:build !unix

package main

// Where the system has no O_NOFOLLOW, a secret file is opened as any file: the
// guard is the descriptor's own Stat and the exclusive creation.
const (
	oNoFollow = 0
	oNonBlock = 0
)
