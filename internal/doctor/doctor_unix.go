//go:build !windows

package doctor

import "syscall"

// errConnRefused is the dial failure that means nothing is listening.
const errConnRefused = syscall.ECONNREFUSED
