//go:build darwin || linux

package observe

import (
	"fmt"
	"os"
	"syscall"
)

func openSource(path string) (*os.File, error) {
	// Nonblocking prevents a replacement FIFO from hanging between discovery and
	// open; NOFOLLOW rejects a symlink in the final path component. Ancestor
	// directory symlinks are allowed: the explicitly selected path is trusted.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("source must be a regular file")
	}
	return f, nil
}
