//go:build !darwin

package services

import "errors"

func cloneIndexFile(source, target string) error {
	return errors.New("filesystem clone unavailable")
}
