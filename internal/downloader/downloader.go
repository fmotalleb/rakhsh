package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Downloader struct {
	client      *http.Client
	maxRetries  uint
	retryDelay  time.Duration
	maxFileSize uint64
}

func New(client *http.Client, maxRetries uint, retryDelay time.Duration, maxFileSize uint64) *Downloader {
	return &Downloader{client: client, maxRetries: maxRetries, retryDelay: retryDelay, maxFileSize: maxFileSize}
}

func (d *Downloader) Download(ctx context.Context, sourceURL, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}
	partPath := destination + ".part"

	var lastErr error
	for attempt := uint(0); attempt <= d.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, d.retryDelay); err != nil {
				return err
			}
		}
		if err := d.downloadOnce(ctx, sourceURL, partPath); err != nil {
			lastErr = err
			continue
		}
		if err := os.Rename(partPath, destination); err != nil {
			return fmt.Errorf("finalize file: %w", err)
		}
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("download failed with unknown error")
	}
	return fmt.Errorf("download failed after retries: %w", lastErr)
}

func (d *Downloader) downloadOnce(ctx context.Context, sourceURL, partPath string) error {
	offset, err := fileSize(partPath)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("unexpected response code: %d", resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent && offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}

	file, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("open target file: %w", err)
	}
	defer file.Close()

	written, err := io.Copy(file, io.LimitReader(resp.Body, limitForCopy(d.maxFileSize, offset)))
	if err != nil {
		return fmt.Errorf("stream to disk: %w", err)
	}
	if d.maxFileSize > 0 && uint64(written)+uint64(offset) >= d.maxFileSize {
		if _, readErr := resp.Body.Read(make([]byte, 1)); readErr == nil {
			return fmt.Errorf("file exceeds max size: %d", d.maxFileSize)
		}
	}
	return nil
}

func limitForCopy(maxSize uint64, current int64) int64 {
	if maxSize == 0 {
		return 1<<63 - 1
	}
	if uint64(current) >= maxSize {
		return 0
	}
	return int64(maxSize - uint64(current))
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat file: %w", err)
	}
	return info.Size(), nil
}

func ParseFilenameFromURL(rawURL string) string {
	segments := strings.Split(rawURL, "/")
	if len(segments) == 0 {
		return "download.bin"
	}
	name := segments[len(segments)-1]
	if name == "" || name == "." || name == ".." {
		return "download.bin"
	}
	if idx := strings.Index(name, "?"); idx >= 0 {
		name = name[:idx]
	}
	if name == "" {
		return "download.bin"
	}
	return name
}

func ExtractSizeHeader(resp *http.Response) int64 {
	if resp == nil {
		return -1
	}
	value := resp.Header.Get("Content-Length")
	if value == "" {
		return -1
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return -1
	}
	return size
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
