//go:build !darwin && !linux

package observe

import (
	"fmt"
	"os"
)

func openDirectory(path string) (*os.File, error) {
	return nil, fmt.Errorf("directory attachment currently supports macOS and Linux only")
}

func openSource(path string) (*os.File, error) {
	return nil, fmt.Errorf("passive file attachment currently supports macOS and Linux only")
}
