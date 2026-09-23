package main

import (
	"fmt"
	"testing"
)

func TestLogSinkSince(t *testing.T) {
	s := newLogSink()
	for i := 0; i < 5; i++ {
		s.Write([]byte(fmt.Sprintf("line %d\n", i)))
	}
	lines, next, gen := s.Since(0)
	if len(lines) != 5 || next != 5 || gen != 0 || lines[0] != "line 0" {
		t.Fatalf("Since(0) = %v, %d, %d", lines, next, gen)
	}
	s.Write([]byte("line 5\n"))
	lines, next, _ = s.Since(next)
	if len(lines) != 1 || lines[0] != "line 5" || next != 6 {
		t.Fatalf("incremental Since = %v, %d", lines, next)
	}
	s.Clear()
	lines, next, gen = s.Since(next)
	if len(lines) != 0 || next != 6 || gen != 1 {
		t.Fatalf("after Clear: %v, %d, %d", lines, next, gen)
	}
	s.Write([]byte("after\n"))
	lines, _, _ = s.Since(0) // older than retained: returns what's kept
	if len(lines) != 1 || lines[0] != "after" {
		t.Fatalf("Since(0) after clear = %v", lines)
	}
}

func TestLogSinkBounded(t *testing.T) {
	s := newLogSink()
	big := make([]byte, 1<<20) // 1 MB per line
	for i := range big {
		big[i] = 'x'
	}
	big[len(big)-1] = '\n'
	for i := 0; i < 60; i++ {
		s.Write(big)
	}
	n, b := s.Stats()
	if b > logBufferMax || n > 50 {
		t.Fatalf("buffer not bounded: %d lines, %d bytes", n, b)
	}
	if n < 40 {
		t.Fatalf("buffer evicted too much: %d lines", n)
	}
}
