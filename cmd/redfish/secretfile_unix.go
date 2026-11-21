//go:build unix

package main

import "syscall"

// Flags of a secret file. O_NOFOLLOW refuses a symlink as the last element of
// the path, O_NONBLOCK keeps the open of a FIFO planted there from hanging
// (the descriptor is then refused as not regular).
const (
	oNoFollow = syscall.O_NOFOLLOW
	oNonBlock = syscall.O_NONBLOCK
)
