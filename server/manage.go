package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
)

//go:embed manage.html
var managementPage []byte

// managementSettings contains the preferences editable from the DSM launcher.
type managementSettings struct {
	PageSize     int
	AutoPlayNext bool
}

func validPageSize(size int) bool {
	switch size {
	case 24, 48, 100, 250, 500, 1000:
		return true
	default:
		return false
	}
}

func (s *Server) libraryPageSize() int {
	var raw string
	if err := s.db.QueryRow("SELECT body FROM prefs WHERE id='management'").Scan(&raw); err != nil {
		return s.cfg.DefaultPageSize
	}
	var settings managementSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil || !validPageSize(settings.PageSize) {
		return s.cfg.DefaultPageSize
	}
	return settings.PageSize
}

func (s *Server) serveManagementPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodHead {
		// The page is public; all status and settings requests require a token.
		_, _ = w.Write(managementPage)
	}
}

func (s *Server) managementSettings(w http.ResponseWriter, r *http.Request) {
	if !s.administrator() {
		http.Error(w, "Administrator access required", 403)
		return
	}
	if r.Method != http.MethodPost {
		configuration := s.user()["Configuration"].(M)
		writeJSON(w, http.StatusOK, managementSettings{
			PageSize:     s.libraryPageSize(),
			AutoPlayNext: configuration["EnableNextEpisodeAutoPlay"] == true,
		})
		return
	}

	var settings managementSettings
	if err := decode(r, &settings); err != nil || !validPageSize(settings.PageSize) {
		http.Error(w, "invalid page size", http.StatusBadRequest)
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "settings unavailable", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	configuration := M{}
	var raw string
	err = tx.QueryRow("SELECT body FROM prefs WHERE id=?", s.storageKey("userconfiguration")).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "settings unavailable", http.StatusInternalServerError)
		return
	}
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &configuration); err != nil {
			http.Error(w, "saved configuration is invalid", http.StatusInternalServerError)
			return
		}
	}
	if configuration == nil {
		configuration = M{}
	}
	configuration["EnableNextEpisodeAutoPlay"] = settings.AutoPlayNext
	for key, value := range map[string]any{
		"management":        settings,
		"userconfiguration": configuration,
	} {
		body, err := json.Marshal(value)
		if err != nil {
			http.Error(w, "cannot encode settings", http.StatusInternalServerError)
			return
		}
		storageKey := key
		if key == "userconfiguration" {
			storageKey = s.storageKey(key)
		}
		_, err = tx.Exec("INSERT INTO prefs VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", storageKey, string(body))
		if err != nil {
			http.Error(w, "cannot save settings", http.StatusInternalServerError)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "cannot save settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
