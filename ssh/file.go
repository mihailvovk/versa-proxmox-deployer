package ssh

import (
	"io"
	"os"
)

// fileWrapper wraps os.File to match our interface
type fileWrapper struct {
	*os.File
}

func (fw *fileWrapper) Stat() (interface{ Size() int64 }, error) {
	return fw.File.Stat()
}

func init() {
	// Initialize the file operation functions
	openFile = func(path string) (interface {
		io.Reader
		io.Closer
		Stat() (interface{ Size() int64 }, error)
	}, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return &fileWrapper{f}, nil
	}

	writeFile = func(path string, data []byte) error {
		return os.WriteFile(path, data, 0644)
	}
}
