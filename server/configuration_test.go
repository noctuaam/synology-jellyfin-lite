package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationRequiresAdminAndAppliesAfterRestart(t *testing.T) {
	s, _ := fixture(t)
	_, token := createViewer(t, s)
	if w := accountRequest(s, "GET", "/NAS/Configuration", "", token); w.Code != 403 {
		t.Fatal(w.Code)
	}
	changes := configurationChanges(s.cfg)
	changes.ServerName = "Configured name"
	for i := range changes.Roots {
		changes.Roots[i].LocalPath = t.TempDir()
	}
	body, _ := json.Marshal(changes)
	w := call(s, "POST", "/NAS/Configuration", string(body), true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.cfg.ServerName == changes.ServerName {
		t.Fatal("live configuration changed before restart")
	}
	configured := s.cfg
	if err := loadConfigurationChanges(&configured); err != nil {
		t.Fatal(err)
	}
	if configured.ServerName != changes.ServerName {
		t.Fatal("saved configuration not applied")
	}
	changes.PublicURL = "https://user:password@example.org/"
	body, _ = json.Marshal(changes)
	if w = call(s, "POST", "/NAS/Configuration", string(body), true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	changes.PublicURL = "https://media.example.org/"
	changes.DLNAUserAgent = "invalid\r\nheader"
	if validateConfiguration(changes) == nil {
		t.Fatal("header injection accepted")
	}
}

func TestLegacyMigrationBackupPreservesCommittedState(t *testing.T) {
	s, _ := fixture(t)
	if _, err := s.db.Exec("INSERT INTO progress VALUES('legacy-item',42,0,1,'yesterday'); DROP TABLE account_tokens; DROP TABLE accounts"); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeAccounts(); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(s.cfg.Data, "backups", "before-multiuser-*", "catalog.db"))
	if err != nil || len(matches) != 1 {
		t.Fatal(matches, err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(matches[0]), "credentials.json"))
	if err != nil || !strings.Contains(string(data), s.cred.Hash) {
		t.Fatal("credentials snapshot missing", err)
	}
	var favorite, position int
	if err := s.db.QueryRow("SELECT favorite,position FROM progress WHERE id='legacy-item'").Scan(&favorite, &position); err != nil || favorite != 1 || position != 42 {
		t.Fatal("migration lost state", err)
	}
	snapshot, err := sql.Open("sqlite", matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var storedPosition int
	if err = snapshot.QueryRow("SELECT position FROM progress WHERE id='legacy-item'").Scan(&storedPosition); err != nil || storedPosition != 42 {
		t.Fatal("backup missed committed WAL state", err)
	}
	snapshot.Close()
	// A repeated start must not produce another migration backup.
	if err := s.initializeAccounts(); err != nil {
		t.Fatal(err)
	}
	again, _ := filepath.Glob(filepath.Join(s.cfg.Data, "backups", "before-multiuser-*", "catalog.db"))
	if len(again) != 1 {
		t.Fatal("migration repeated")
	}
	// Exercise restoration only in this disposable fixture directory.
	if _, err = s.db.Exec("UPDATE progress SET position=99 WHERE id='legacy-item'"); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"catalog.db-wal", "catalog.db-shm"} {
		if err = os.Remove(filepath.Join(s.cfg.Data, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.cfg.Data, "catalog.db"), saved, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := openServer(s.cfg, s.cred)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.db.Close()
	if err = restored.db.QueryRow("SELECT position FROM progress WHERE id='legacy-item'").Scan(&storedPosition); err != nil || storedPosition != 42 {
		t.Fatal("snapshot restoration lost legacy progress", err)
	}
}

func TestViewerStatusDoesNotCountOtherLibraries(t *testing.T) {
	s, _ := fixture(t)
	_, token := createViewer(t, s)
	result := jsonBody(t, accountRequest(s, "GET", "/System/Index/Status", "", token))
	counts := result["Counts"].(map[string]any)
	if counts["Photo"] != nil || counts["Audio"] != nil {
		t.Fatal("other library counts leaked", counts)
	}
}
