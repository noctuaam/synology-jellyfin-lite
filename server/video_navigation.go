package main

import (
	_ "embed"
	"net/http"
)

//go:embed video-navigation.js
var videoNavigationScript []byte

func (s *Server) serveVideoNavigation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodHead {
		// A disconnected client needs no further response.
		_, _ = w.Write(videoNavigationScript)
	}
}
