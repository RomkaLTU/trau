package doctor

import "golang.org/x/sys/windows"

// errConnRefused is the dial failure that means nothing is listening. Winsock
// reports WSAECONNREFUSED, a different errno from the syscall.ECONNREFUSED Unix
// returns, so matching the Unix one alone reads every free port as occupied.
const errConnRefused = windows.WSAECONNREFUSED
