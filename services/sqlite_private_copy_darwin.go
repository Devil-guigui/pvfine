package services

import "golang.org/x/sys/unix"

// APFS clones share disk blocks until either file is written.
func cloneIndexFile(source, target string) error {
	return unix.Clonefile(source, target, 0)
}
