package phpm

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// No symlinks, hard links, devices, traversal or overwrites are accepted.
func extractTarGZIP(archive, target string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	const maxExpanded = int64(1024 * 1024 * 1024)
	bounded := &io.LimitedReader{R: gz, N: maxExpanded + 1}
	reader := tar.NewReader(bounded)
	for entries := 0; ; entries++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if entries >= 20000 {
			return errors.New("archive contains too many entries")
		}
		if header.Typeflag == tar.TypeDir && (header.Name == "." || header.Name == "./") {
			continue
		}
		rel, err := safeRelative(header.Name)
		if err != nil {
			return err
		}
		if header.Size < 0 || header.Size > maxExpanded {
			return errors.New("invalid or excessive TAR entry size")
		}
		dest := filepath.Join(target, rel)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|os.FileMode(header.Mode)&0111)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(out, reader)
			closeErr := out.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
			if n != header.Size {
				return errors.New("TAR entry size mismatch")
			}
		default:
			return errors.New("archive contains a link or special file")
		}
	}
	// Read through the trailer so gzip CRC/truncation failures cannot be hidden
	// behind the TAR end marker. This also bounds trailing decompressed bytes.
	if _, err := io.Copy(io.Discard, bounded); err != nil {
		return err
	}
	if bounded.N == 0 {
		return errors.New("expanded archive exceeds 1 GiB limit")
	}
	return nil
}
