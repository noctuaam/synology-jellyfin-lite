package main

import (
	_ "embed"
	"net/http"
)

//go:embed legacy-photo.js
var legacyPhotoScript []byte

func (s *Server) serveLegacyPhotoScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodHead {
		// Disconnects can fail the write; there is no further response to send.
		_, _ = w.Write(legacyPhotoScript)
	}
}
