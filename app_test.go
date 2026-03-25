package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func TestResilientMultiWriterContinuesAfterFailure(t *testing.T) {
	var fileSink bytes.Buffer
	var remoteSink bytes.Buffer

	writer := newResilientMultiWriter(failingWriter{}, &fileSink, &remoteSink)
	payload := []byte("test log line\n")

	n, err := writer.Write(payload)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d bytes, want %d", n, len(payload))
	}
	if got := fileSink.String(); got != string(payload) {
		t.Fatalf("file sink got %q, want %q", got, string(payload))
	}
	if got := remoteSink.String(); got != string(payload) {
		t.Fatalf("remote sink got %q, want %q", got, string(payload))
	}
}

func TestResilientMultiWriterErrorsWhenAllWritersFail(t *testing.T) {
	writer := newResilientMultiWriter(failingWriter{}, shortWriter{})

	n, err := writer.Write([]byte("test"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Write error = %v, want %v", err, io.ErrShortWrite)
	}
	if n != 0 {
		t.Fatalf("Write returned %d bytes, want 0", n)
	}
}
