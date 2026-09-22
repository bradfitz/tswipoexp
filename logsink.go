package main

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"
)

// logSink fans log lines out to stderr, the current profile's log
// file, and an in-memory ring for the debug endpoint.
type logSink struct {
	mu   sync.Mutex
	file *os.File
	ring [][]byte
	next int
}

const logRingSize = 2000

func newLogSink() *logSink {
	return &logSink{ring: make([][]byte, logRingSize)}
}

// SetFile switches the log file to the one in the given profile
// directory, closing any previous one.
func (s *logSink) SetFile(dir string) error {
	f, err := openLogFile(dir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		s.file.Close()
	}
	s.file = f
	return nil
}

// Logf formats and writes one log line with a timestamp.
func (s *logSink) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if len(msg) == 0 || msg[len(msg)-1] != '\n' {
		msg += "\n"
	}
	s.Write([]byte(time.Now().UTC().Format("2006/01/02 15:04:05.000 ") + msg))
}

// Write implements io.Writer for use with the log package. Each call
// is treated as one line.
func (s *logSink) Write(p []byte) (int, error) {
	line := bytes.Clone(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	os.Stderr.Write(line)
	if s.file != nil {
		s.file.Write(line)
	}
	s.ring[s.next] = line
	s.next = (s.next + 1) % len(s.ring)
	return len(p), nil
}

// Lines returns the last n log lines, oldest first.
func (s *logSink) Lines(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.ring) {
		n = len(s.ring)
	}
	var out []string
	for i := 0; i < len(s.ring) && len(out) < n; i++ {
		idx := (s.next - 1 - i + len(s.ring)) % len(s.ring)
		if s.ring[idx] == nil {
			break
		}
		out = append(out, string(s.ring[idx]))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
