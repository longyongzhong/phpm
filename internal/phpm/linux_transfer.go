package phpm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var staticPHPContentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)

// Transfer bounded chunks from the publisher's range-capable object storage.
// A partial network read can resume only at bytes actually received. No cache
// entry is committed until the full published SHA-256 has been verified.
func (p linuxRuntimeProvider) download(ctx context.Context, raw, checksum string) (string, error) {
	if err := staticPHPURL(raw); err != nil {
		return "", err
	}
	if !validSHA(checksum) {
		return "", errors.New("download requires a SHA-256 checksum")
	}
	checksum = strings.ToLower(checksum)
	unlock, err := p.Store.lock("download:" + checksum)
	if err != nil {
		return "", err
	}
	defer unlock()
	target := filepath.Join(p.Store.Home, "cache", checksum+".zip")
	if existing, err := fileSHA(target); err == nil {
		if existing != checksum {
			return "", errors.New("cached download is corrupted; remove the cached file and retry")
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Join(p.Store.Home, "cache"), ".staticphp-*")
	if err != nil {
		return "", err
	}
	defer func() { temp.Close(); os.Remove(temp.Name()) }()
	var offset, total int64
	noProgress := 0
	for total == 0 || offset < total {
		data, size, readErr, protocolErr := p.readRange(ctx, raw, offset, total)
		if protocolErr != nil {
			return "", protocolErr
		}
		if len(data) == 0 {
			noProgress++
			if ctx.Err() != nil {
				return "", fmt.Errorf("StaticPHP transfer stopped at %d/%d bytes: %w", offset, total, ctx.Err())
			}
			if noProgress >= 3 {
				return "", fmt.Errorf("StaticPHP download made no progress at byte %d: %w", offset, readErr)
			}
			continue
		}
		noProgress = 0
		if _, err := temp.Write(data); err != nil {
			return "", err
		}
		previous := offset
		offset += int64(len(data))
		total = size
		if p.Progress != nil && (offset/(1024*1024) != previous/(1024*1024) || offset == total) {
			fmt.Fprintf(p.Progress, "Downloaded PHP: %d/%d bytes\n", offset, total)
		}
		if readErr != nil && p.Progress != nil {
			fmt.Fprintf(p.Progress, "Resuming interrupted PHP transfer at %d/%d bytes...\n", offset, total)
		}
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	actual, err := fileSHA(temp.Name())
	if err != nil {
		return "", err
	}
	if actual != checksum {
		return "", errors.New("download SHA-256 mismatch; archive was not installed")
	}
	if err := os.Rename(temp.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

func (p linuxRuntimeProvider) readRange(ctx context.Context, raw string, start, knownTotal int64) (data []byte, total int64, readErr, protocolErr error) {
	return readPackageRange(ctx, p.Client, raw, start, knownTotal)
}

func readPackageRange(ctx context.Context, client *http.Client, raw string, start, knownTotal int64) (data []byte, total int64, readErr, protocolErr error) {
	const chunk = int64(1024 * 1024)
	const maxDownload = int64(512 * 1024 * 1024)
	partCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(partCtx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("User-Agent", "PHPM/"+Version)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+chunk-1))
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, err, nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		return nil, 0, nil, fmt.Errorf("PHP package range request returned HTTP %d, expected 206", response.StatusCode)
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return nil, 0, nil, errors.New("PHP package range response unexpectedly uses content encoding")
	}
	fields := staticPHPContentRange.FindStringSubmatch(response.Header.Get("Content-Range"))
	if fields == nil {
		return nil, 0, nil, errors.New("PHP package range response has no valid Content-Range")
	}
	values := make([]int64, 3)
	for i := range values {
		values[i], err = strconv.ParseInt(fields[i+1], 10, 64)
		if err != nil {
			return nil, 0, nil, err
		}
	}
	from, to, total := values[0], values[1], values[2]
	if from != start || total <= 0 || total > maxDownload || to < from || to >= total || to-start >= chunk || (knownTotal != 0 && total != knownTotal) {
		return nil, 0, nil, errors.New("PHP package range response has unexpected offset/size or changed archive length")
	}
	length := to - from + 1
	if response.ContentLength >= 0 && response.ContentLength != length {
		return nil, 0, nil, errors.New("PHP package Content-Length differs from Content-Range")
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, length+1))
	if int64(len(data)) > length {
		return nil, 0, nil, errors.New("PHP package range body exceeds its declared size")
	}
	if int64(len(data)) < length && err == nil {
		err = io.ErrUnexpectedEOF
	}
	return data, total, err, nil
}
