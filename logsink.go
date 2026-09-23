package main

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"
)

// logSink fans log lines out to stderr, the current profile's log
// file, and an in-memory buffer for the log viewer and the debug
// endpoint. The buffer keeps the most recent lines up to logBufferMax
// bytes, and every line has a sequence number so viewers can fetch
// only what's new.
type logSink struct {
	mu    sync.Mutex
	file  *os.File
	lines []string // oldest first
	bytes int      // total length of lines
	first uint64   // sequence number of lines[0]
	next  uint64   // sequence number the next line will get
	gen   uint64   // bumped by Clear so viewers can reset
}

// logBufferMax caps the in-memory log buffer.
const logBufferMax = 50 << 20

func newLogSink() *logSink {
	return &logSink{}
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
	line := string(bytes.TrimRight(p, "\r\n"))
	s.mu.Lock()
	defer s.mu.Unlock()
	os.Stderr.Write(p)
	if s.file != nil {
		s.file.Write(p)
	}
	s.lines = append(s.lines, line)
	s.bytes += len(line)
	s.next++
	for s.bytes > logBufferMax && len(s.lines) > 1 {
		s.bytes -= len(s.lines[0])
		s.lines[0] = ""
		s.lines = s.lines[1:]
		s.first++
	}
	return len(p), nil
}

// Lines returns the last n log lines, oldest first, each with a
// trailing newline.
func (s *logSink) Lines(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.lines) {
		n = len(s.lines)
	}
	out := make([]string, 0, n)
	for _, l := range s.lines[len(s.lines)-n:] {
		out = append(out, l+"\n")
	}
	return out
}

// Since returns the lines with sequence numbers at or after seq,
// the sequence number to pass next time, and the current generation.
// If the generation differs from what the caller last saw, the
// buffer was cleared and the caller should drop what it has. If seq
// is older than what's retained, the oldest retained lines are
// returned.
func (s *logSink) Since(seq uint64) (lines []string, next uint64, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq < s.first {
		seq = s.first
	}
	if seq > s.next {
		seq = s.next
	}
	lines = append([]string(nil), s.lines[seq-s.first:]...)
	return lines, s.next, s.gen
}

// Clear drops the in-memory buffer (the log file is untouched) and
// bumps the generation.
func (s *logSink) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = nil
	s.bytes = 0
	s.first = s.next
	s.gen++
}

// Stats returns the number of buffered lines and bytes.
func (s *logSink) Stats() (lines, bytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines), s.bytes
}
