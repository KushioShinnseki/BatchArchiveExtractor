package main

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed vendor/7z.exe vendor/7z.dll vendor/License.txt
var assets embed.FS

func unpackEngine() (string, func(), error) {
	dir, err := os.MkdirTemp("", "batch-unzip-engine-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	for _, name := range []string{"7z.exe", "7z.dll", "License.txt"} {
		data, err := assets.ReadFile("vendor/" + name)
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return filepath.Join(dir, "7z.exe"), cleanup, nil
}
