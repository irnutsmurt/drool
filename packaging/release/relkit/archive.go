package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// cmdTar writes a reproducible .tar.gz: entries sorted by name, the given
// modification time, owner root, modes 0755 (executable) or 0644, and a
// gzip header without a name or time.
func cmdTar(args []string) error {
	fs := flag.NewFlagSet("tar", flag.ContinueOnError)
	out := fs.String("out", "", "output .tar.gz")
	prefix := fs.String("prefix", "", "directory inside the archive (e.g. drool_1.0.0_linux_amd64/)")
	epoch := fs.Int64("epoch", -1, "modification time of every entry (SOURCE_DATE_EPOCH)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || *epoch < 0 || fs.NArg() == 0 {
		return errors.New("usage: relkit tar -out FILE -prefix DIR/ -epoch N SRC[=NAME]...")
	}
	type entry struct{ src, name string }
	var files []entry
	for _, a := range fs.Args() {
		src, name, ok := strings.Cut(a, "=")
		if !ok {
			name = path.Base(src)
		}
		files = append(files, entry{src, name})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeTarGz(f, *prefix, time.Unix(*epoch, 0), func(add func(name string, mode int64, r io.Reader, size int64) error) error {
		for _, e := range files {
			fi, err := os.Stat(e.src)
			if err != nil {
				return err
			}
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", e.src)
			}
			mode := int64(0o644)
			if fi.Mode()&0o111 != 0 {
				mode = 0o755
			}
			r, err := os.Open(e.src)
			if err != nil {
				return err
			}
			err = add(e.name, mode, r, fi.Size())
			r.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return f.Close()
}

func writeTarGz(w io.Writer, prefix string, mtime time.Time, fill func(add func(name string, mode int64, r io.Reader, size int64) error) error) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	gz.OS = 3 // Unix, whatever the build machine
	tw := tar.NewWriter(gz)
	hdr := func(name string, mode int64, typ byte, size int64) *tar.Header {
		return &tar.Header{Name: name, Mode: mode, Typeflag: typ, Size: size, ModTime: mtime, Uname: "root", Gname: "root", Format: tar.FormatUSTAR}
	}
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/") + "/"
		if err := tw.WriteHeader(hdr(prefix, 0o755, tar.TypeDir, 0)); err != nil {
			return err
		}
	}
	err = fill(func(name string, mode int64, r io.Reader, size int64) error {
		if err := tw.WriteHeader(hdr(prefix+name, mode, tar.TypeReg, size)); err != nil {
			return err
		}
		n, err := io.Copy(tw, r)
		if err == nil && n != size {
			err = fmt.Errorf("%s changed while archiving", name)
		}
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
