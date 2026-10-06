package autoupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path string, names ...string) {
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip Create(%s): %+v", name, err)
		}
		if _, err := entry.Write([]byte("payload")); err != nil {
			t.Fatalf("zip Write: %+v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip Close: %+v", err)
	}
}

type tarEntry struct {
	name     string
	typeflag byte
	linkname string
}

func writeTarGz(t *testing.T, path string, entries ...tarEntry) {
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %+v", err)
	}
	defer file.Close()
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Linkname: entry.linkname, Mode: 0o644}
		if entry.typeflag == tar.TypeReg {
			header.Size = int64(len("payload"))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("tar WriteHeader(%s): %+v", entry.name, err)
		}
		if entry.typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte("payload")); err != nil {
				t.Fatalf("tar Write: %+v", err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("tar Close: %+v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip Close: %+v", err)
	}
}

// TestExtractArchiveStaysInsideDestination pins that archive entries cannot be written outside the extraction
// directory. Entry names and tar symlink targets were joined onto the destination unchecked, so a release archive
// with ../ entries, or with a symlink leading out of the directory, wrote files wherever the node user could.
func TestExtractArchiveStaysInsideDestination(t *testing.T) {
	tests := map[string]func(t *testing.T, archive string){
		"zip dotdot entry": func(t *testing.T, archive string) {
			writeZip(t, archive, "htnd", "../../escaped")
		},
		"tar dotdot entry": func(t *testing.T, archive string) {
			writeTarGz(t, archive, tarEntry{name: "htnd", typeflag: tar.TypeReg},
				tarEntry{name: "../../escaped", typeflag: tar.TypeReg})
		},
		"tar symlink out of destination": func(t *testing.T, archive string) {
			writeTarGz(t, archive, tarEntry{name: "htnd", typeflag: tar.TypeReg},
				tarEntry{name: "link", typeflag: tar.TypeSymlink, linkname: "../.."},
				tarEntry{name: "link/escaped", typeflag: tar.TypeReg})
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			// destination is root/a/b/dest, so ../../escaped from it lands in root/a and ../.. from a link in
			// dest resolves to root/a as well.
			destination := filepath.Join(root, "a", "b", "dest")
			extension := ".zip"
			if name != "zip dotdot entry" {
				extension = ".tar.gz"
			}
			archive := filepath.Join(root, "release"+extension)
			build(t, archive)

			_, _ = ExtractArchive(archive, destination)

			for _, escaped := range []string{
				filepath.Join(root, "a", "escaped"),
				filepath.Join(root, "a", "b", "escaped"),
			} {
				if _, err := os.Lstat(escaped); err == nil {
					t.Fatalf("archive entry was written outside the extraction directory: %s", escaped)
				}
			}
		})
	}
}
