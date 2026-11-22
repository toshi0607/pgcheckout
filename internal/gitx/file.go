package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/toshi0607/pgtwig/internal/config"
)

const managedHeader = "# managed by pgtwig; do not edit"

func absolutePath(cwd, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Abs(path)
}

func readFile(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, info, nil
	}
	data, err := os.ReadFile(path)
	return data, info, err
}

func unmanagedFile(path string) *config.Error {
	return config.NewError(config.UnmanagedFile, "file is not managed by pgtwig", fmt.Sprintf("Move %q out of the way and retry.", path))
}

func writeAtomic(path, content string, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pgtwig-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp.Name()))
		}
	}()
	_, err = tmp.WriteString(content)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
