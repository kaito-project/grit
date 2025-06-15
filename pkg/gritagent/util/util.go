// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package util

import (
	"os"
	"path/filepath"
)

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
