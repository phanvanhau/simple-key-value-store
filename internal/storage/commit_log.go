package storage

import (
	"bufio"
	"fmt"
	"os"
)

// Note: WAL access is intended to be single-writer during normal operation,
// with readers only during crash recovery. Current helpers do not coordinate
// concurrent writers/readers; add external synchronization if used outside
// that pattern.

// Write appends bytes to the given open file handle. Caller owns file lifecycle.
func Write(file *os.File, data []byte) error {
	writer := bufio.NewWriter(file)
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

// Read reads length bytes starting from the given offset on the provided file handle.
func Read(file *os.File, offset int64, length int) ([]byte, error) {
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}

	reader := bufio.NewReader(file)
	buf := make([]byte, length)
	n, err := reader.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return nil, fmt.Errorf("read: %w", err)
	}
	return buf[:n], nil
}
