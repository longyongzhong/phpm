package phpm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticPHPRangeDownloadResumesAndVerifiesWholeArchive(t *testing.T) {
	s := testStore(t)
	data := bytes.Repeat([]byte("verified PHP archive bytes"), 10000)
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	requests := 0
	var starts []int64
	p := linuxRuntimeProvider{Store: s, Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		var start, end int64
		if _, err := fmt.Sscanf(req.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Accept-Encoding") != "identity" {
			t.Fatal("range transfer may change encoded bytes")
		}
		starts = append(starts, start)
		if end >= int64(len(data)) {
			end = int64(len(data)) - 1
		}
		part := data[start : end+1]
		declared := int64(len(part))
		if requests == 1 {
			part = part[:12345]
		} // Inject a truncated first response.
		return &http.Response{StatusCode: 206, ContentLength: declared, Header: http.Header{"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, len(data))}}, Body: io.NopCloser(bytes.NewReader(part)), Request: req}, nil
	})}}
	path, err := p.download(context.Background(), staticPHPDownloadBase+"php.tar.gz", checksum)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, data) || len(starts) < 2 || starts[1] != 12345 {
		t.Fatalf("resumption lost bytes: %v %+v", err, starts)
	}
	before := requests
	if _, err := p.download(context.Background(), staticPHPDownloadBase+"php.tar.gz", checksum); err != nil || requests != before {
		t.Fatalf("verified cache not reused: %v", err)
	}
	if err := os.WriteFile(path, []byte("corrupted cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.download(context.Background(), staticPHPDownloadBase+"php.tar.gz", checksum); err == nil || requests != before {
		t.Fatal("corrupted cache was accepted or silently replaced")
	}
}

func TestStaticPHPRangeProtocolAndNoProgressFailures(t *testing.T) {
	for _, test := range []struct {
		status                       int
		contentRange, encoding, body string
		length                       int64
	}{
		{200, "", "", "x", 1},
		{206, "bytes 1-1/2", "", "x", 1},
		{206, "bytes 0-1/536870913", "", "xx", 2},
		{206, "bytes 0-1/2", "gzip", "xx", 2},
		{206, "bytes 0-1/2", "", "xx", 1},
		{206, "bytes 0-0/1", "", "xx", -1},
	} {
		p := linuxRuntimeProvider{Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, ContentLength: test.length, Header: http.Header{"Content-Range": []string{test.contentRange}, "Content-Encoding": []string{test.encoding}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: req}, nil
		})}}
		if _, _, _, err := p.readRange(context.Background(), staticPHPDownloadBase+"php.tar.gz", 0, 0); err == nil {
			t.Fatalf("invalid response accepted: %+v", test)
		}
	}
	s := testStore(t)
	requests := 0
	p := linuxRuntimeProvider{Store: s, Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 206, ContentLength: 1, Header: http.Header{"Content-Range": []string{"bytes 0-0/1"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}}
	if _, err := p.download(context.Background(), staticPHPDownloadBase+"php.tar.gz", strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "no progress") || requests != 3 {
		t.Fatalf("no-progress transfer did not stop: %v (%d)", err, requests)
	}
	entries, err := os.ReadDir(filepath.Join(s.Home, "cache"))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed transfer left a cache/partial file")
	}
}
