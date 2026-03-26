package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
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

func TestResolveConfigPathPrefersExistingPrimaryConfig(t *testing.T) {
	cwd := t.TempDir()
	exeDir := t.TempDir()

	existing := filepath.Join(exeDir, primaryConfigFileName)
	if err := os.WriteFile(existing, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	got := resolveConfigPathForLocations(cwd, exeDir, fileExists)
	if got != existing {
		t.Fatalf("resolveConfigPathForLocations() = %q, want %q", got, existing)
	}
}

func TestResolveConfigPathFallsBackToExeDir(t *testing.T) {
	cwd := t.TempDir()
	exeDir := t.TempDir()

	got := resolveConfigPathForLocations(cwd, exeDir, fileExists)
	want := filepath.Join(exeDir, primaryConfigFileName)
	if got != want {
		t.Fatalf("resolveConfigPathForLocations() = %q, want %q", got, want)
	}
}

func TestLoadValidatedConfigReturnsNotExistForMissingFile(t *testing.T) {
	app := &App{
		configPath: filepath.Join(t.TempDir(), primaryConfigFileName),
	}

	_, err := app.loadValidatedConfig()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loadValidatedConfig() error = %v, want %v", err, os.ErrNotExist)
	}

	if app.HasConfig() {
		t.Fatalf("HasConfig() = true, want false")
	}
}

func TestServiceStartupDefersWindowShowWhenConfigMissing(t *testing.T) {
	app := &App{
		configPath: filepath.Join(t.TempDir(), primaryConfigFileName),
		lastStatus: "idle",
	}

	if err := app.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("ServiceStartup() error = %v", err)
	}

	if got := app.GetStatus(); got != "config not found" {
		t.Fatalf("GetStatus() = %q, want %q", got, "config not found")
	}
	if !app.shouldShowOnStart() {
		t.Fatalf("shouldShowOnStart() = false, want true")
	}
}
