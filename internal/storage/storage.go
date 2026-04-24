package storage

import (
	"crypto/md5" //nolint:gosec // we don't need strong cryptographic primitive
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var filenameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type Storage struct {
	root string
}

func New(root string) *Storage {
	return &Storage{root: root}
}

func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o750)
}

func (s *Storage) Root() string {
	return s.root
}

func (s *Storage) TempPath(originalName string) string {
	return filepath.Join(s.root, "tmp_"+sanitizeName(originalName))
}

func (s *Storage) Finalize(tempPath, originalName string) (storedName string, md5Hex string, err error) {
	md5Hex, err = FileMD5(tempPath)
	if err != nil {
		return "", "", err
	}
	storedName = AddHashSuffix(originalName, md5Hex)
	finalPath := filepath.Join(s.root, storedName)
	if statErr := os.Rename(tempPath, finalPath); statErr != nil {
		if _, existsErr := os.Stat(finalPath); existsErr == nil {
			if removeErr := os.Remove(tempPath); removeErr != nil {
				return "", "", fmt.Errorf("remove duplicate temp file: %w", removeErr)
			}
			return storedName, md5Hex, nil
		}
		return "", "", fmt.Errorf("move file to final location: %w", statErr)
	}
	return storedName, md5Hex, nil
}

func FileMD5(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // needs to be from variable
	if err != nil {
		return "", fmt.Errorf("open file for md5: %w", err)
	}
	defer file.Close()

	hasher := md5.New() //nolint:gosec // not super important
	if _, err = io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func AddHashSuffix(originalName, md5Hex string) string {
	name := sanitizeName(originalName)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		base = "file"
	}
	if ext == "" {
		return fmt.Sprintf("%s_%s", base, md5Hex)
	}
	return fmt.Sprintf("%s_%s%s", base, md5Hex, ext)
}

func ExtractHashFromName(name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	idx := strings.LastIndex(base, "_")
	if idx < 0 || idx+1 >= len(base) {
		return ""
	}
	candidate := base[idx+1:]
	if len(candidate) != 32 {
		return ""
	}
	for _, ch := range candidate {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	return candidate
}

func sanitizeName(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return "file.bin"
	}
	input = strings.ReplaceAll(input, "\\", "_")
	input = strings.ReplaceAll(input, "/", "_")
	input = filenameSanitizer.ReplaceAllString(input, "_")
	if input == "" || input == "." || input == ".." {
		return "file.bin"
	}
	return input
}
