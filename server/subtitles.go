package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func textSubtitle(stream M) bool {
	codec, _ := stream["Codec"].(string)
	switch strings.ToLower(codec) {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text":
		return stream["Type"] == "Subtitle"
	}
	return false
}

func streamIndex(stream M) int {
	switch index := stream["Index"].(type) {
	case int:
		return index
	case float64:
		return int(index)
	}
	return -1
}

func (s *Server) playbackInfo(w http.ResponseWriter, r *http.Request, media Media) {
	source := s.source(media)
	options := M{}
	if r.Method == "POST" {
		if err := decode(r, &options); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "Invalid playback options", 400)
			return
		}
	}
	var selectedAudio, selectedSubtitle *int
	for _, kind := range []string{"Audio", "Subtitle"} {
		key := kind + "StreamIndex"
		index := -2
		if value, ok := options[key].(float64); ok && value == float64(int(value)) {
			index = int(value)
		}
		if value := query(r, key); value != "" {
			if parsed, err := strconv.Atoi(value); err == nil {
				index = parsed
			}
		}
		valid := kind == "Subtitle" && index == -1
		for _, stream := range media.Streams {
			if stream["Type"] == kind && streamIndex(stream) == index {
				valid = true
			}
		}
		if valid {
			source["Default"+key] = index
			if kind == "Audio" {
				selectedAudio = &index
			} else {
				selectedSubtitle = &index
			}
		}
	}
	if err := s.rememberStreams(media, selectedAudio, selectedSubtitle); err != nil {
		http.Error(w, "Cannot save track selection", 500)
		return
	}
	writeJSON(w, 200, M{
		"MediaSources":  []M{source},
		"PlaySessionId": randomHex(16),
	})
}

// Browsers cannot read text tracks embedded in MKV. Advertise a separate VTT
// resource while keeping the original stream indices used by the track picker.
func (s *Server) playbackStreams(media Media) []M {
	streams := make([]M, 0, len(media.Streams))
	for _, original := range media.Streams {
		stream := M{}
		for key, value := range original {
			stream[key] = value
		}
		if textSubtitle(stream) {
			index := streamIndex(stream)
			stream["Codec"] = "webvtt"
			stream["DeliveryMethod"] = "External"
			stream["IsTextSubtitle"] = true
			stream["SupportsExternalStream"] = true
			stream["IsExternalUrl"] = false
			stream["DeliveryUrl"] = fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/Stream.vtt?uid=%s&tag=%s", media.ID, media.ID, index, s.profileID(), s.subtitleTag(media, index))
		}
		streams = append(streams, stream)
	}
	return streams
}

func (s *Server) subtitleTag(media Media, index int) string {
	hash := hmac.New(sha256.New, []byte(s.profileSigningKey()))
	fmt.Fprintf(hash, "subtitle-profile-v1\x00%s\x00%s", s.profileID(), s.subtitleCacheKey(media, index))
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

// Cache bytes are shared, while each delivery URL has account-specific access.
// This key also retains subtitle files prepared by the single-user release.
func (s *Server) subtitleCacheKey(media Media, index int) string {
	hash := hmac.New(sha256.New, []byte(s.cred.Hash))
	fmt.Fprintf(hash, "subtitle-v1\x00%s\x00%d\x00%d\x00%d", media.ID, index, media.Size, media.MTime)
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func subtitleRoute(path string) (string, int, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 6 || !strings.EqualFold(parts[0], "Videos") || parts[1] != parts[2] || !strings.EqualFold(parts[3], "Subtitles") || (!strings.EqualFold(parts[5], "Stream.vtt") && !strings.EqualFold(parts[5], "Stream.js")) {
		return "", 0, false
	}
	index, err := strconv.Atoi(parts[4])
	return parts[1], index, err == nil && index >= 0
}

// HTML track requests cannot attach our authentication header. A signed URL
// authorizes only this item's particular subtitle, never the movie itself.
func (s *Server) serveSignedSubtitle(w http.ResponseWriter, r *http.Request, path string) bool {
	id, index, ok := subtitleRoute(path)
	if !ok || (r.Method != "GET" && r.Method != "HEAD") {
		return false
	}
	accountID := query(r, "uid")
	if accountID == "" {
		accountID = userID
	}
	view, valid := s.accountView(accountID)
	if !valid {
		return false
	}
	media, err := view.media(id)
	if err != nil {
		return false
	}
	expected := view.subtitleTag(media, index)
	if query(r, "uid") == "" {
		if view.identity.Hash != s.cred.Hash {
			return false
		}
		expected = view.subtitleCacheKey(media, index)
	}
	if !hmac.Equal([]byte(query(r, "Tag")), []byte(expected)) {
		return false
	}
	view.subtitle(w, r, path)
	return true
}

func (s *Server) subtitle(w http.ResponseWriter, r *http.Request, path string) {
	id, index, ok := subtitleRoute(path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	media, err := s.media(id)
	if err != nil || media.Kind != "Video" {
		http.NotFound(w, r)
		return
	}
	found := false
	for _, stream := range media.Streams {
		if streamIndex(stream) == index && textSubtitle(stream) {
			found = true
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	cache := filepath.Join(s.cfg.Data, "subtitles", s.subtitleCacheKey(media, index)+".vtt")
	if _, err = os.Stat(cache); os.IsNotExist(err) {
		// Keep the small NAS responsive: at most one extraction process, and
		// never decode or transcode any video/audio stream.
		if !s.subtitleMu.TryLock() {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "Another subtitle is being prepared; retry shortly", 503)
			return
		}
		err = s.extractSubtitle(r.Context(), media, index, cache)
		s.subtitleMu.Unlock()
		if err != nil {
			log.Printf("Subtitle extraction failed for item %s stream %d: %v", media.ID, index, err)
			http.Error(w, "Subtitle extraction failed", 502)
			return
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	// Jellyfin Web renders text captions itself on several platforms. Its
	// fetchSubtitles() replaces .vtt with .js and expects tick-based JSON.
	if strings.HasSuffix(strings.ToLower(path), ".js") {
		events, err := readSubtitleEvents(cache)
		if err != nil {
			http.Error(w, "Invalid subtitle cache", 502)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == "HEAD" {
			w.WriteHeader(200)
			return
		}
		writeJSON(w, 200, M{"TrackEvents": events})
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeFile(w, r, cache)
}

// SubtitleEvent is the JSON cue format consumed by Jellyfin Web's HTML player.
type SubtitleEvent struct {
	StartPositionTicks int64
	EndPositionTicks   int64
	Text               string
}

func readSubtitleEvents(cache string) ([]SubtitleEvent, error) {
	file, err := os.Open(cache)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, errors.New("subtitle exceeds cache size limit")
	}
	return parseSubtitleEvents(string(data))
}

func parseSubtitleEvents(text string) ([]SubtitleEvent, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text != "WEBVTT" && !strings.HasPrefix(text, "WEBVTT\n") && !strings.HasPrefix(text, "WEBVTT ") && !strings.HasPrefix(text, "WEBVTT\t") {
		return nil, errors.New("missing WebVTT header")
	}
	events := []SubtitleEvent{}
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 || strings.HasPrefix(lines[0], "WEBVTT") || lines[0] == "STYLE" || lines[0] == "REGION" || lines[0] == "NOTE" || strings.HasPrefix(lines[0], "NOTE ") {
			continue
		}
		timing := 0
		if !strings.Contains(lines[0], "-->") {
			timing = 1 // optional cue identifier
		}
		parts := strings.SplitN(lines[timing], "-->", 2)
		if len(parts) != 2 || len(lines) <= timing+1 {
			continue
		}
		start, err := subtitleTicks(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, err
		}
		endFields := strings.Fields(parts[1])
		if len(endFields) == 0 {
			return nil, errors.New("missing cue end time")
		}
		end, err := subtitleTicks(endFields[0])
		if err != nil || end < start {
			return nil, errors.New("invalid cue end time")
		}
		events = append(events, SubtitleEvent{start, end, strings.Join(lines[timing+1:], "\n")})
	}
	return events, nil
}

func subtitleTicks(timestamp string) (int64, error) {
	parts := strings.Split(timestamp, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, errors.New("invalid cue timestamp")
	}
	var seconds int64
	for i, part := range parts[:len(parts)-1] {
		value, err := strconv.ParseInt(part, 10, 32)
		if err != nil || value < 0 || value > 999999 || (i == len(parts)-2 && value >= 60) {
			return 0, errors.New("invalid cue timestamp")
		}
		seconds = seconds*60 + value
	}
	last := strings.Split(parts[len(parts)-1], ".")
	if len(last) != 2 || len(last[1]) != 3 {
		return 0, errors.New("invalid cue milliseconds")
	}
	second, err := strconv.ParseInt(last[0], 10, 32)
	if err != nil || second < 0 || second >= 60 {
		return 0, errors.New("invalid cue seconds")
	}
	milliseconds, err := strconv.ParseInt(last[1], 10, 32)
	if err != nil || milliseconds < 0 || milliseconds > 999 {
		return 0, errors.New("invalid cue milliseconds")
	}
	return ((seconds*60+second)*1000 + milliseconds) * 10000, nil
}

func (s *Server) subtitleArguments(media Media, index int) ([]string, error) {
	// Use a readable indexed local file when it resolves inside a configured
	// video root. This avoids routing a full MKV through the DLNA HTTP server.
	if local := s.localSubtitleInput(media); local != "" {
		return subtitleCommandArguments(local, index), nil
	}
	input, err := s.mediaURL(media.SourceURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(input)
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("albumArt", "false")
	u.RawQuery = query.Encode()
	return subtitleCommandArguments(u.String(), index), nil
}

func subtitleCommandArguments(input string, index int) []string {
	return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "1", "-i", input, "-map", fmt.Sprintf("0:%d", index), "-vn", "-an", "-c:s", "webvtt", "-f", "webvtt", "pipe:1"}
}

func (s *Server) localSubtitleInput(media Media) string {
	if media.Path == "" || !filepath.IsAbs(media.Path) {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(media.Path)
	if err != nil {
		return ""
	}
	for _, root := range s.cfg.Roots {
		if root.Key != "video" || root.LocalPath == "" {
			continue
		}
		base, err := filepath.EvalSymlinks(root.LocalPath)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(base, resolved)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		file, err := os.Open(resolved)
		if err == nil {
			file.Close()
			return resolved
		}
	}
	return ""
}

func (s *Server) extractSubtitle(parent context.Context, media Media, index int, cache string) error {
	if _, err := os.Stat(cache); err == nil {
		return nil
	}
	args, err := s.subtitleArguments(media, index)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(cache), ".extract-*.vtt")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	binary := s.cfg.FFmpeg
	if binary == "" {
		binary = "/var/packages/MediaServer/target/bin/ffmpeg"
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout = &subtitleWriter{
		writer:    file,
		remaining: 8 << 20,
	}
	var diagnostic bytes.Buffer
	command.Stderr = &subtitleWriter{
		writer:    &diagnostic,
		remaining: 4096,
	}
	if err = command.Run(); err != nil {
		return fmt.Errorf("extract subtitle: %w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var header [6]byte
	if _, err = io.ReadFull(file, header[:]); err != nil || string(header[:]) != "WEBVTT" {
		return fmt.Errorf("invalid WebVTT output")
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), cache)
}

type subtitleWriter struct {
	writer    io.Writer
	remaining int
}

func (w *subtitleWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, fmt.Errorf("subtitle exceeds cache size limit")
	}
	n, err := w.writer.Write(data)
	w.remaining -= n
	return n, err
}
