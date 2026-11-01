package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagementSettingsRequireAuthenticationAndPreserveOtherPreferences(t *testing.T) {
	s, _ := fixture(t)
	if response := call(s, "GET", "/manage/", "", false); response.Code != 200 || !strings.Contains(response.Body.String(), "a.voronov") {
		t.Fatal("management page unavailable", response.Code)
	}
	for _, method := range []string{"GET", "POST"} {
		if response := call(s, method, "/NAS/Settings", `{"PageSize":48}`, false); response.Code != 401 {
			t.Fatal("anonymous settings access", response.Code)
		}
	}
	_, err := s.db.Exec("INSERT INTO prefs VALUES('userconfiguration',?)", `{"SubtitleMode":"Always","EnableNextEpisodeAutoPlay":true}`)
	if err != nil {
		t.Fatal(err)
	}
	response := call(s, "POST", "/NAS/Settings", `{"PageSize":48,"AutoPlayNext":false}`, true)
	if response.Code != 200 || s.libraryPageSize() != 48 {
		t.Fatal("settings not saved", response.Code, response.Body.String())
	}
	configuration := s.user()["Configuration"].(M)
	if configuration["EnableNextEpisodeAutoPlay"] != false {
		t.Fatal("auto playback preference not applied")
	}
	var raw string
	if err = s.db.QueryRow("SELECT body FROM prefs WHERE id='userconfiguration'").Scan(&raw); err != nil || !strings.Contains(raw, `"SubtitleMode":"Always"`) {
		t.Fatal("unrelated preference was lost", raw, err)
	}
	settings := jsonBody(t, call(s, "GET", "/NAS/Settings", "", true))
	if settings["PageSize"] != float64(48) || settings["AutoPlayNext"] != false {
		t.Fatal(settings)
	}
	for _, body := range []string{`{"PageSize":100000}`, `{"PageSize":0}`, `{"PageSize":"48"}`} {
		if response = call(s, "POST", "/NAS/Settings", body, true); response.Code != 400 || s.libraryPageSize() != 48 {
			t.Fatal("invalid settings accepted", response.Code)
		}
	}
}

func TestManagementPageSizeControlsClientBootstrap(t *testing.T) {
	s, _ := fixture(t)
	s.cfg.Web = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.cfg.Web, "index.html"), []byte("<html><head></head></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	response := call(s, "GET", "/web/index.html", "", false)
	if !strings.Contains(response.Body.String(), `-libraryPageSize","1000"`) {
		t.Fatal("default page size changed")
	}
	response = call(s, "POST", "/NAS/Settings", `{"PageSize":250,"AutoPlayNext":true}`, true)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	response = call(s, "GET", "/web/index.html", "", false)
	if !strings.Contains(response.Body.String(), `-libraryPageSize","250"`) {
		t.Fatal("client bootstrap did not use saved page size")
	}
}
