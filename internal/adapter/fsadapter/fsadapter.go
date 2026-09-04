package fsadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	MkdirAll(path string) error
	// Mode answers a file's permission bits, and Chmod sets them. A copy
	// that does not carry them writes a file nothing can execute: a built
	// binary arrives, and the run that needs it dies on "permission
	// denied" rather than on anything about the binary.
	Mode(path string) (os.FileMode, error)
	Chmod(path string, mode os.FileMode) error
	Exists(path string) (bool, error)
	// IsDir answers whether path is a directory. A missing path is false
	// and not an error, like Exists.
	IsDir(path string) (bool, error)
	List(dir string) ([]string, error)
	Walk(dir string) ([]string, error)
	Remove(path string) error
}

type OS struct{}

var _ FS = OS{}

func New() OS {
	return OS{}
}

func (OS) ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return data, nil
}

func (OS) WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

func (OS) MkdirAll(path string) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}

	return nil
}

func (OS) Mode(path string) (os.FileMode, error) {
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return 0, fmt.Errorf("reading the mode of %s: %w", path, err)
	}

	return info.Mode().Perm(), nil
}

func (OS) Chmod(path string, mode os.FileMode) error {
	if err := os.Chmod(filepath.Clean(path), mode); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", path, err)
	}

	return nil
}

func (OS) Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, fmt.Errorf("inspecting %s: %w", path, err)
}

func (OS) IsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir(), nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, fmt.Errorf("inspecting %s: %w", path, err)
}

func (OS) List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	sort.Strings(names)

	return names, nil
}

// Walk returns the relative slash paths of every file under dir, sorted. A
// missing directory walks to nothing, matching List.
func (OS) Walk(dir string) ([]string, error) {
	var names []string

	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}

			return err
		}

		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}

		names = append(names, filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", dir, err)
	}

	sort.Strings(names)

	return names, nil
}

func (OS) Remove(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing %s: %w", path, err)
	}

	return nil
}

// Digest measures one file: its sha256 hex and its size. The distribution
// index is built from these, so it never claims a byte nobody hashed.
func Digest(path string) (string, int64, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", 0, fmt.Errorf("digesting %s: %w", path, err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}
