package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"openmediad/internal/system/layout"
)

// Manifest — что установка изменила в rootfs: по нему uninstall возвращает колонку как было.
type Manifest struct {
	Version   string  `json:"version"`
	Installed string  `json:"installed"` // время первой установки (RFC 3339)
	Updated   string  `json:"updated,omitempty"`
	Files     []Entry `json:"files"`
}

// Entry — один файл rootfs.
type Entry struct {
	Path    string `json:"path"`                  // путь на колонке
	Existed bool   `json:"existed"`               // файл был до установки: его копия — Backup
	Backup  string `json:"backup,omitempty"`      // копия оригинала (имя в backup/)
	OrigSHA string `json:"orig_sha256,omitempty"` // sha256 оригинала
	SHA     string `json:"sha256,omitempty"`      // sha256 того, что записали мы
	Link    string `json:"link,omitempty"`        // мы создали символьную ссылку: куда она указывает
}

// ManifestPath — где лежит манифест.
func ManifestPath() string { return filepath.Join(layout.Backup(), "manifest.json") }

// LoadManifest читает манифест; нет файла — nil без ошибки.
func LoadManifest() (*Manifest, error) {
	b, err := os.ReadFile(ManifestPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save записывает манифест атомарно.
func (m *Manifest) Save() error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(ManifestPath(), append(b, '\n'), 0o644)
}

func (m *Manifest) entry(path string) *Entry {
	for i := range m.Files {
		if m.Files[i].Path == path {
			return &m.Files[i]
		}
	}
	return nil
}

func (m *Manifest) remove(path string) {
	out := m.Files[:0]
	for _, e := range m.Files {
		if e.Path != path {
			out = append(out, e)
		}
	}
	m.Files = out
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func fileSHA(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha(b), nil
}

// writeAtomic пишет файл через .new и переименование, со сбросом на диск.
func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
