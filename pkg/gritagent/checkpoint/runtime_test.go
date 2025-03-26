// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"context"
	"os"
	"path"
	"testing"
)

func TestWriteContainerLog(t *testing.T) {
	ctx := context.Background()

	t.Run("log directory does not exist", func(t *testing.T) {
		err := writeContainerLog(ctx, "/nonexistent/logdir", "/tmp/container.log")
		if err == nil {
			t.Fatalf("expected not exist error, got %v", err)
		}
	})

	t.Run("log directory is empty", func(t *testing.T) {
		tempDir := t.TempDir()
		err := writeContainerLog(ctx, tempDir, "/tmp/container.log")
		if err != nil {
			t.Fatalf("expected no error for empty log directory, got %v", err)
		}
	})

	t.Run("log directory contains valid log files", func(t *testing.T) {
		tempDir := t.TempDir()
		logFile1 := path.Join(tempDir, "0.log")
		logFile2 := path.Join(tempDir, "1.log")
		os.WriteFile(logFile1, []byte("log1"), 0644)
		os.WriteFile(logFile2, []byte("log2"), 0644)

		savePath := path.Join(tempDir, "saved.log")
		err := writeContainerLog(ctx, tempDir, savePath)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		content, err := os.ReadFile(savePath)
		if err != nil {
			t.Fatalf("failed to read saved log file: %v", err)
		}

		if string(content) != "log2" {
			t.Fatalf("expected log2 content, got %s", string(content))
		}
	})

	t.Run("log directory contains non-log files", func(t *testing.T) {
		tempDir := t.TempDir()
		nonLogFile := path.Join(tempDir, "container1.txt")
		os.WriteFile(nonLogFile, []byte("non-log content"), 0644)

		savePath := path.Join(tempDir, "saved.log")
		err := writeContainerLog(ctx, tempDir, savePath)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}

		_, err = os.Stat(savePath)
		if err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected no saved log file, got %v", err)
		}
	})
}
