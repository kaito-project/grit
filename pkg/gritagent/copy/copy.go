// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package copy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/multierr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TransferData(ctx context.Context, srcDir, dstDir string) error {
	var wg sync.WaitGroup
	errs := make([]error, 20)
	workerChan := make(chan struct{}, 10)

	log.FromContext(ctx).Info("start to transfer data", "src-dir", srcDir, "dst-dir", dstDir)
	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dstDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, os.ModePerm)
		}

		wg.Add(1)
		workerChan <- struct{}{}
		go func(src, dst string) {
			defer func() {
				wg.Done()
				<-workerChan
			}()

			if err := copyFile(src, dst); err != nil {
				errs = append(errs, err)
			}
			log.FromContext(ctx).Info("copy file successfully", "src-file", src)
		}(path, dstPath)

		return nil
	})

	if err != nil {
		return err
	}

	wg.Wait()
	log.FromContext(ctx).Info("data transfer completed", "src-dir", srcDir, "dst-dir", dstDir)

	return multierr.Combine(errs...)
}

func copyFile(srcFile, dstFile string) error {
	src, err := os.Open(srcFile)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstFile)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	if err != nil {
		return err
	}

	info, err := os.Stat(srcFile)
	if err != nil {
		return err
	}

	return os.Chmod(dstFile, info.Mode())
}

func CreateSentinelFile(dir, fileName string) error {
	filePath := filepath.Join(dir, fileName)
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString("Transfer Completed\n")
	return err
}
