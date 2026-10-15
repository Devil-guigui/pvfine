package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetTextEncodingRefreshesArchiveAndCacheIdentity(t *testing.T) {
	c := NewCore()
	t.Cleanup(c.closeArchive)
	svc := NewArchiveService(c)
	if _, err := svc.SetTextEncoding("GBK"); err != ErrNoArchive {
		t.Fatalf("without archive: %v", err)
	}
	if _, err := svc.Open(writeSearchFixture(t, "encoding.pvf")); err != nil {
		t.Fatal(err)
	}
	waitForSearchIndex(t, c)
	original := c.archive
	before, err := searchIndexCacheIdentityForArchive(original)
	if err != nil {
		t.Fatal(err)
	}
	sqliteBefore, _, err := archiveIndexIdentity(original)
	if err != nil {
		t.Fatal(err)
	}
	c.editorText[0] = "stale cache"
	info, err := svc.SetTextEncoding("GBK")
	if err != nil {
		t.Fatal(err)
	}
	waitForSearchIndex(t, c)
	if info.TextEncoding != "GBK" || info.ModifiedCount != 0 {
		t.Fatalf("unexpected state: %#v", info)
	}
	c.mu.RLock()
	current := c.archive
	cached := len(c.editorText)
	c.mu.RUnlock()
	if cached != 0 {
		t.Fatal("editor text cache retained old decoding")
	}
	after, err := searchIndexCacheIdentityForArchive(current)
	if err != nil || before == after {
		t.Fatal("search cache identity unchanged", err)
	}
	sqliteAfter, _, err := archiveIndexIdentity(current)
	if err != nil || sqliteBefore == sqliteAfter {
		t.Fatal("SQLite identity unchanged", err)
	}
	if _, err := svc.SetTextEncoding("invalid"); err == nil {
		t.Fatal("accepted invalid encoding")
	}
	if svc.Info().TextEncoding != "GBK" {
		t.Fatal("failed switch changed archive")
	}
	c.mu.Lock()
	if err := current.SetText(0, "1008 `changed.equ`"); err != nil {
		c.mu.Unlock()
		t.Fatal(err)
	}
	c.mu.Unlock()
	if _, err := svc.SetTextEncoding("auto"); err == nil {
		t.Fatal("discarded pending edits")
	}
}

func TestArchiveTextEncodingPersistsAcrossRestart(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	path := writeSearchFixture(t, "remember-encoding.pvf")
	otherPath := writeSearchFixture(t, "other-encoding.pvf")
	c := NewCore(newSettingsService(settingsPath))
	svc := NewArchiveService(c)
	t.Cleanup(c.closeArchive)
	if _, err := svc.Open(path); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetTextEncoding("UTF-16LE"); err != nil {
		t.Fatal(err)
	}
	svc.Close()

	// A fresh core and settings service model an application restart.
	next := NewCore(newSettingsService(settingsPath))
	t.Cleanup(next.closeArchive)
	reopened := NewArchiveService(next)
	info, err := reopened.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.TextEncoding != "UTF-16LE" {
		t.Fatalf("reopened encoding = %q", info.TextEncoding)
	}
	info, err = reopened.Open(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.TextEncoding != "auto" {
		t.Fatal("encoding leaked to another archive")
	}
	if _, err := reopened.Open(path); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.SetTextEncoding("invalid"); err == nil {
		t.Fatal("accepted invalid codec")
	}
	if value, err := next.settings.archiveTextEncoding(path); err != nil || value != "UTF-16LE" {
		t.Fatal("failed switch changed remembered codec", value, err)
	}
	if _, err := reopened.SetTextEncoding("auto"); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	info, err = svc.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.TextEncoding != "auto" {
		t.Fatal("automatic selection was not remembered")
	}
}

func TestArchiveTextEncodingCanonicalPathAndSettingsIsolation(t *testing.T) {
	directory := t.TempDir()
	settings := newSettingsService(filepath.Join(directory, "settings.json"))
	path := writeSearchFixture(t, "encoding-source.pvf")
	alias := filepath.Join(t.TempDir(), "alias.pvf")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := settings.rememberArchiveTextEncoding(path, "Big5"); err != nil {
		t.Fatal(err)
	}
	if value, err := settings.archiveTextEncoding(alias); err != nil || value != "Big5" {
		t.Fatal("symlink did not share preference", value, err)
	}
	if err := settings.SaveSettings(DefaultAppSettings()); err != nil {
		t.Fatal(err)
	}
	if value, err := newSettingsService(settings.path).archiveTextEncoding(path); err != nil || value != "Big5" {
		t.Fatal("general settings erased preference", value, err)
	}
}

func TestArchiveTextEncodingPersistenceFailureKeepsCurrentArchive(t *testing.T) {
	directory := t.TempDir()
	settings := newSettingsService(filepath.Join(directory, "settings.json"))
	c := NewCore(settings)
	t.Cleanup(c.closeArchive)
	svc := NewArchiveService(c)
	if _, err := svc.Open(writeSearchFixture(t, "encoding-failure.pvf")); err != nil {
		t.Fatal(err)
	}
	// A directory cannot be read or replaced as the preference file.
	if err := os.Mkdir(settings.archiveEncodingPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	original := c.archive
	if _, err := svc.SetTextEncoding("GBK"); err == nil {
		t.Fatal("persistence failure was ignored")
	}
	if c.archive != original || svc.Info().TextEncoding != "auto" {
		t.Fatal("failed switch changed active archive")
	}
}
