package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// mockStdin replaces os.Stdin with a string reader for testing
func mockStdin(t *testing.T, input string) func() {
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	
	os.Stdin = r
	
	go func() {
		defer w.Close()
		w.WriteString(input)
	}()
	
	return func() {
		os.Stdin = oldStdin
		r.Close()
	}
}

// captureOutput captures stdout during function execution
func captureOutput(t *testing.T, fn func()) string {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	
	os.Stdout = w
	
	var buf bytes.Buffer
	done := make(chan bool)
	
	go func() {
		io.Copy(&buf, r)
		done <- true
	}()
	
	fn()
	
	w.Close()
	os.Stdout = oldStdout
	<-done
	
	return buf.String()
}

// createTempFile creates a temporary file with given content for testing
func createTempFile(t *testing.T, content string) string {
	tmpFile, err := os.CreateTemp("", "omet_test_*.txt")
	require.NoError(t, err)
	
	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	
	err = tmpFile.Close()
	require.NoError(t, err)
	
	// Clean up after test
	t.Cleanup(func() {
		os.Remove(tmpFile.Name())
	})
	
	return tmpFile.Name()
}

// createTestApp creates a CLI app instance for testing
func createTestApp() *cli.App {
	app := &cli.App{
		Name:  "omet",
		Usage: "OpenMetrics manipulation tool",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "file",
				Aliases: []string{"f"},
				Usage:   "Input metrics file (default: stdin)",
				Value:   "-",
			},
			&cli.StringSliceFlag{
				Name:    "label",
				Aliases: []string{"l"},
				Usage:   "Add label in KEY=VALUE format (can be repeated)",
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Enable verbose logging",
			},
		},
		ArgsUsage: "<metric_name> <operation> [value]",
		Action:    runOmet,
	}
	return app
}
