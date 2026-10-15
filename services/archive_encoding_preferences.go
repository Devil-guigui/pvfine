package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"pvfine/internal/pvf"
)

// Archive preferences live separately so saving general settings cannot erase
// selections made by another window with an older settings snapshot.
func (s *SettingsService) archiveEncodingPath() string {
	return filepath.Join(filepath.Dir(s.path), "archive-encodings.json")
}

func (s *SettingsService) readArchiveEncodingsLocked() (map[string]string, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	data, err := os.ReadFile(s.archiveEncodingPath())
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取归档编码记录失败: %w", err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("解析归档编码记录失败: %w", err)
	}
	if values == nil {
		values = map[string]string{}
	}
	return values, nil
}

func (s *SettingsService) archiveTextEncoding(path string) (string, error) {
	if s == nil {
		return "auto", nil
	}
	key, err := canonicalSearchIndexPath(path)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.readArchiveEncodingsLocked()
	if err != nil {
		return "", err
	}
	if value := values[key]; value != "" {
		return value, nil
	}
	return "auto", nil
}

func (s *SettingsService) rememberArchiveTextEncoding(path, encoding string) error {
	if s == nil {
		return nil
	}
	key, err := canonicalSearchIndexPath(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.readArchiveEncodingsLocked()
	if err != nil {
		return err
	}
	if encoding == "auto" || encoding == "" {
		delete(values, key)
	} else {
		values[key] = encoding
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	destination := s.archiveEncodingPath()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".archive-encodings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err := temp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), destination); err != nil {
		return fmt.Errorf("保存归档编码记录失败: %w", err)
	}
	return nil
}

func (c *core) openArchiveWithRememberedTextEncoding(path, sourcePath, sidecarDir string) (*pvf.Archive, error) {
	encoding, err := c.settings.archiveTextEncoding(sourcePath)
	if err != nil {
		return nil, err
	}
	return pvf.OpenWithTextEncoding(path, sidecarDir, encoding)
}
