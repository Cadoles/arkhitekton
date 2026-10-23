package installed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"testing/fstest"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

// Pack writes a plugin's directory as the layer it is distributed in. Entries are sorted and
// undated: packing the same directory twice gives the same digest.
func Pack(fsys fs.FS) ([]byte, error) {
	if err := bounded(fsys, manifest.MaxSize); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw)), ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		_, err = archive.Write(raw)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Unpack reads a layer back into a directory held in memory. Only regular files under clean
// relative paths are accepted, and no more than limit bytes once decompressed: the archive comes
// from a registry, and its signature proves who packed it, not that it is harmless.
func Unpack(layer io.Reader, limit int64) (fs.FS, error) {
	decompressed, err := gzip.NewReader(layer)
	if err != nil {
		return nil, fmt.Errorf("plugin layer: %w", err)
	}
	// fstest.MapFS is the standard library's file system in memory; nothing of the testing package
	// comes with it.
	files := fstest.MapFS{}
	archive := tar.NewReader(decompressed)
	var total int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("plugin layer: %w", err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("plugin layer: %s is not a regular file", header.Name)
		}
		name := path.Clean(header.Name)
		if !fs.ValidPath(name) || name == "." || name != header.Name {
			return nil, fmt.Errorf("plugin layer: %q is not a path inside the plugin", header.Name)
		}
		if _, twice := files[name]; twice {
			return nil, fmt.Errorf("plugin layer: %s packed twice", name)
		}
		if total += header.Size; header.Size < 0 || total > limit {
			return nil, fmt.Errorf("plugin larger than %d MiB", limit>>20)
		}
		raw, err := io.ReadAll(io.LimitReader(archive, header.Size))
		if err != nil {
			return nil, fmt.Errorf("plugin layer: %w", err)
		}
		files[name] = &fstest.MapFile{Data: raw, Mode: 0o644}
	}
	return files, nil
}
