package handover

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// writeTarball gzips the directory at srcDir into a tar.gz at dstPath.
// Entries are stored under the basename of srcDir so the reviewer always
// gets a single top-level folder when they untar the archive.
func writeTarball(dstPath, srcDir string) error {
	root := filepath.Base(srcDir)
	tmp := dstPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	cleanup := func() {
		out.Close()
		os.Remove(tmp)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		name := root
		if rel != "." {
			name = filepath.Join(root, rel)
		}
		// tar uses forward slashes in entry names regardless of OS.
		name = strings.ReplaceAll(name, string(os.PathSeparator), "/")

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
		return nil
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("tar walk: %w", err)
	}
	if err := tw.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close gzip: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close tarball: %w", err)
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename tarball: %w", err)
	}
	return nil
}
