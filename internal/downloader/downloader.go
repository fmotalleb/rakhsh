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

	toollog "github.com/fmotalleb/go-tools/log"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/internal/helper"
)

type Downloader struct {
	client      *http.Client
	maxRetries  uint
	retryDelay  time.Duration
	maxFileSize uint64
}

type Progress struct {
	Downloaded int64
	Total      int64
	Attempt    uint

	CurrentSpeed float64 // bytes/sec
	AvgSpeed     float64 // bytes/sec
	Elapsed      time.Duration
	ETA          time.Duration
}

func (p Progress) String() string {
	speed := helper.HumanBytes(int64(p.CurrentSpeed)) + "/s"
	avg := helper.HumanBytes(int64(p.AvgSpeed)) + "/s"

	if p.Total > 0 {
		percent := float64(p.Downloaded) * 100 / float64(p.Total)
		return fmt.Sprintf(
			`%s / %s (%.1f%%)
Speed: %s
Average: %s
Elapsed: %s
ETA: %s
attempt=%d`,
			helper.HumanBytes(p.Downloaded),
			helper.HumanBytes(p.Total),
			percent,
			speed,
			avg,
			p.Elapsed.Truncate(time.Second),
			p.ETA.Truncate(time.Second),
			p.Attempt,
		)
	}

	return fmt.Sprintf(
		"%s speed=%s avg=%s elapsed=%s [attempt %d]",
		helper.HumanBytes(p.Downloaded),
		speed,
		avg,
		p.Elapsed.Truncate(time.Second),
		p.Attempt,
	)
}

type ProgressFunc func(Progress)

func New(client *http.Client, maxRetries uint, retryDelay time.Duration, maxFileSize uint64) *Downloader {
	return &Downloader{client: client, maxRetries: maxRetries, retryDelay: retryDelay, maxFileSize: maxFileSize}
}

func (d *Downloader) Download(ctx context.Context, sourceURL, destination string) error {
	return d.DownloadWithProgress(ctx, sourceURL, destination, 2*time.Second, nil)
}

func (d *Downloader) DownloadWithProgress(
	ctx context.Context,
	sourceURL string,
	destination string,
	interval time.Duration,
	progressCb ProgressFunc,
) error {
	logger := toollog.Of(ctx)
	logger.Debug("download started",
		zap.String("source_url", sourceURL),
		zap.String("destination", destination),
		zap.Uint("max_retries", d.maxRetries),
		zap.Duration("retry_delay", d.retryDelay),
		zap.Uint64("max_file_size", d.maxFileSize),
	)
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}
	partPath := destination + ".part"

	var lastErr error
	for attempt := uint(0); attempt <= d.maxRetries; attempt++ {
		logger.Debug("download attempt",
			zap.Uint("attempt", attempt+1),
			zap.Uint("max_attempts", d.maxRetries+1),
			zap.String("source_url", sourceURL),
			zap.String("part_path", partPath),
		)
		if attempt > 0 {
			if err := sleepContext(ctx, d.retryDelay); err != nil {
				return err
			}
		}
		if err := d.downloadOnce(ctx, sourceURL, partPath, attempt+1, interval, progressCb); err != nil {
			logger.Debug("download attempt failed",
				zap.Uint("attempt", attempt+1),
				zap.Error(err),
			)
			lastErr = err
			continue
		}
		if err := os.Rename(partPath, destination); err != nil {
			return fmt.Errorf("finalize file: %w", err)
		}
		logger.Debug("download completed",
			zap.String("source_url", sourceURL),
			zap.String("destination", destination),
		)
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("download failed with unknown error")
	}
	return fmt.Errorf("download failed after retries: %w", lastErr)
}

func (d *Downloader) downloadOnce(
	ctx context.Context,
	sourceURL string,
	partPath string,
	attempt uint,
	interval time.Duration,
	progressCb ProgressFunc,
) error {
	logger := toollog.Of(ctx)
	offset, err := fileSize(partPath)
	if err != nil {
		return err
	}
	logger.Debug("resume state", zap.Int64("offset", offset), zap.String("part_path", partPath))

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
	logger.Debug("download response received",
		zap.Int("status_code", resp.StatusCode),
		zap.String("content_range", resp.Header.Get("Content-Range")),
		zap.String("content_length", resp.Header.Get("Content-Length")),
	)

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("unexpected response code: %d", resp.StatusCode)
	}
	totalSize := responseTotalSize(resp, offset)
	logger.Debug("resolved total size", zap.Int64("total_size", totalSize), zap.Int64("offset", offset))
	if progressCb != nil {
		progressCb(Progress{Downloaded: offset, Total: totalSize, Attempt: attempt})
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent && offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}

	file, err := os.OpenFile(partPath, flags, 0o600) //nolint:gosec // needs to be from variable
	if err != nil {
		return fmt.Errorf("open target file: %w", err)
	}
	defer file.Close()

	reader := io.LimitReader(resp.Body, limitForCopy(d.maxFileSize, offset))
	if progressCb != nil {
		reader = &progressReader{
			reader:     reader,
			downloaded: offset,
			total:      totalSize,
			attempt:    attempt,
			startTime:  time.Now(),
			lastEmit:   time.Now(),
			lastBytes:  offset,
			interval:   interval,
			progressCb: progressCb,
		}
	}
	written, err := io.Copy(file, reader)
	if err != nil {
		return fmt.Errorf("stream to disk: %w", err)
	}
	logger.Debug("chunk downloaded",
		zap.Int64("written_this_attempt", written),
		zap.Int64("offset_before", offset),
		zap.Int64("downloaded_total_after", offset+written),
	)

	if d.maxFileSize > 0 && uint64(written)+uint64(offset) >= d.maxFileSize { //nolint:gosec // unimportant conversion
		if _, readErr := resp.Body.Read(make([]byte, 1)); readErr == nil {
			return fmt.Errorf("file exceeds max size: %d", d.maxFileSize)
		}
	}
	return nil
}

func responseTotalSize(resp *http.Response, offset int64) int64 {
	if resp == nil {
		return -1
	}
	if resp.StatusCode == http.StatusPartialContent {
		rangeHeader := resp.Header.Get("Content-Range")
		if rangeHeader != "" {
			parts := strings.Split(rangeHeader, "/")
			if len(parts) == 2 {
				total, err := strconv.ParseInt(parts[1], 10, 64)
				if err == nil {
					return total
				}
			}
		}
	}
	length := ExtractSizeHeader(resp)
	if length < 0 {
		return -1
	}
	if resp.StatusCode == http.StatusPartialContent {
		return offset + length
	}
	return length
}

type progressReader struct {
	reader     io.Reader
	downloaded int64
	total      int64
	attempt    uint

	startTime time.Time
	lastEmit  time.Time
	lastBytes int64

	interval   time.Duration
	progressCb ProgressFunc
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.downloaded += int64(n)
		now := time.Now()

		if now.Sub(r.lastEmit) >= r.interval {
			elapsed := now.Sub(r.startTime)
			bytesDelta := r.downloaded - r.lastBytes
			timeDelta := now.Sub(r.lastEmit)

			var currentSpeed float64
			if timeDelta > 0 {
				currentSpeed = float64(bytesDelta) / timeDelta.Seconds()
			}

			var avgSpeed float64
			if elapsed > 0 {
				avgSpeed = float64(r.downloaded) / elapsed.Seconds()
			}

			var eta time.Duration = -1
			if r.total > 0 && avgSpeed > 0 {
				remaining := float64(r.total - r.downloaded)
				eta = time.Duration(remaining/avgSpeed) * time.Second
			}

			r.progressCb(Progress{
				Downloaded:   r.downloaded,
				Total:        r.total,
				Attempt:      r.attempt,
				CurrentSpeed: currentSpeed,
				AvgSpeed:     avgSpeed,
				Elapsed:      elapsed,
				ETA:          eta,
			})

			r.lastEmit = now
			r.lastBytes = r.downloaded
		}
	}

	if err == io.EOF {
		elapsed := time.Since(r.startTime)

		var avgSpeed float64
		if elapsed > 0 {
			avgSpeed = float64(r.downloaded) / elapsed.Seconds()
		}

		r.progressCb(Progress{
			Downloaded:   r.downloaded,
			Total:        r.total,
			Attempt:      r.attempt,
			CurrentSpeed: avgSpeed,
			AvgSpeed:     avgSpeed,
			Elapsed:      elapsed,
			ETA:          0,
		})
	}

	return n, err
}

func limitForCopy(maxSize uint64, current int64) int64 {
	if maxSize == 0 {
		return 1<<63 - 1
	}
	if uint64(current) >= maxSize { //nolint:gosec // unimportant conversion
		return 0
	}
	return int64(maxSize - uint64(current)) //nolint:gosec // unimportant conversion
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
	const downloadBin = "download.bin"
	if len(segments) == 0 {
		return downloadBin
	}
	name := segments[len(segments)-1]
	if name == "" || name == "." || name == ".." {
		return downloadBin
	}
	if idx := strings.Index(name, "?"); idx >= 0 {
		name = name[:idx]
	}
	if name == "" {
		return downloadBin
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
