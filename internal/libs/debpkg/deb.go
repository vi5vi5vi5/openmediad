// Package debpkg — достать файлы из пакета .deb без dpkg: архив ar, внутри data.tar.xz (или .gz, .bz2, .tar).
//
// Нужен компонентам, которые докладывают на колонку чужие библиотеки (spotify-soloist берёт glibc из пакета Debian): на колонке
// нет ни dpkg, ни xz, а в стандартной библиотеке Go нет декодера xz, поэтому используется ulikunitz/xz (vendor/).
package debpkg

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/ulikunitz/xz"
)

// maxFile — самый большой файл, который Extract согласен прочитать в память.
const maxFile = 64 << 20

// File — файл из data.tar пакета.
type File struct {
	Name string      // путь в пакете без ведущих «./» и «/»
	Mode fs.FileMode // права из архива
	Data []byte
}

// Extract читает пакет из r и возвращает обычные файлы, для которых want(имя) истинно. Имена — как в пакете, например
// «usr/lib/arm-linux-gnueabihf/libc.so.6». Ссылки не возвращаются.
func Extract(r io.Reader, want func(name string) bool) ([]File, error) {
	var magic [8]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, fmt.Errorf("deb: %w", err)
	}
	if string(magic[:]) != "!<arch>\n" {
		return nil, errors.New("deb: это не архив ar")
	}
	for {
		var h [60]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("deb: в пакете нет data.tar.*")
			}
			return nil, fmt.Errorf("deb: заголовок ar: %w", err)
		}
		if string(h[58:60]) != "`\n" {
			return nil, errors.New("deb: повреждён заголовок ar")
		}
		name := strings.TrimRight(strings.TrimSpace(string(h[:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(h[48:58])), 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("deb: повреждён размер в заголовке ar")
		}
		body := io.LimitReader(r, size)
		if strings.HasPrefix(name, "data.tar") {
			return extractTar(body, name, want)
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			return nil, fmt.Errorf("deb: %w", err)
		}
		if size%2 == 1 {
			if _, err := io.CopyN(io.Discard, r, 1); err != nil {
				return nil, fmt.Errorf("deb: %w", err)
			}
		}
	}
}

func extractTar(body io.Reader, member string, want func(string) bool) ([]File, error) {
	var tr io.Reader
	switch {
	case strings.HasSuffix(member, ".xz"):
		x, err := xz.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("deb: %s: %w", member, err)
		}
		tr = x
	case strings.HasSuffix(member, ".gz"):
		g, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("deb: %s: %w", member, err)
		}
		tr = g
	case strings.HasSuffix(member, ".bz2"):
		tr = bzip2.NewReader(body)
	case member == "data.tar":
		tr = body
	default:
		return nil, fmt.Errorf("deb: сжатие %s не поддержано (есть xz, gz, bz2)", member)
	}
	var out []File
	t := tar.NewReader(tr)
	for {
		hdr, err := t.Next()
		if errors.Is(err, io.EOF) {
			// дочитать до конца потока: так xz сверит контрольную сумму, а оборванный пакет не сойдёт за целый
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, fmt.Errorf("deb: %s: %w", member, err)
			}
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("deb: %s: %w", member, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(path.Clean("/"+hdr.Name), "/")
		if !want(name) {
			continue
		}
		if hdr.Size < 0 || hdr.Size > maxFile {
			return nil, fmt.Errorf("deb: файл %s слишком большой (%d байт)", name, hdr.Size)
		}
		data, err := io.ReadAll(io.LimitReader(t, hdr.Size))
		if err != nil {
			return nil, fmt.Errorf("deb: %s: %w", name, err)
		}
		out = append(out, File{Name: name, Mode: fs.FileMode(hdr.Mode).Perm(), Data: data})
	}
}
