//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "syscall"

// openNoFollow makes open(2) fail, instead of following it, when the last
// component of a path is a symbolic link.
const openNoFollow = syscall.O_NOFOLLOW
