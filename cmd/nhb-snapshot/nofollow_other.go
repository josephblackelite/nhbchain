//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

// openNoFollow is zero where the platform has no O_NOFOLLOW (Windows, for one):
// openPlain then relies on its own lstat before the open and on comparing the
// opened file with what lstat saw.
const openNoFollow = 0
