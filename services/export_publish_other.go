//go:build !linux && !windows

package services

import (
	"fmt"
	"os"
	"runtime"
)

func publishModDirectory(temp, dest string) error {
	// macOS refuses to rename a directory over an existing directory.
	if runtime.GOOS == "darwin" {
		return os.Rename(temp, dest)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		return fmt.Errorf("无法创建 mod 目录（可能已存在）: %w", err)
	}
	if err := os.Rename(temp, dest); err != nil {
		_ = os.Remove(dest) // Remove refuses a nonempty directory.
		return err
	}
	return nil
}
