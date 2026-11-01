package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const nodeSchema = `CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY,parent TEXT,root TEXT,kind TEXT,name TEXT,sortname TEXT,sourceid TEXT,relpath TEXT,body TEXT);
CREATE INDEX IF NOT EXISTS nodes_parent ON nodes(parent,sortname);
CREATE INDEX IF NOT EXISTS nodes_kind ON nodes(kind);
CREATE TABLE IF NOT EXISTS closure(ancestor TEXT,descendant TEXT,depth INTEGER,PRIMARY KEY(ancestor,descendant));`

func openServer(cfg Config, c Credentials) (*Server, error) {
	configureDefaults(&cfg)
	if e := validateOrigin(cfg.SynologyControlURL); e != nil {
		return nil, e
	}
	if e := validateOrigin(cfg.SynologyMediaBase); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", filepath.Join(cfg.Data, "catalog.db"))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=10000; PRAGMA cache_size=-2048;
 CREATE TABLE IF NOT EXISTS progress(id TEXT PRIMARY KEY,position INTEGER NOT NULL DEFAULT 0,played INTEGER NOT NULL DEFAULT 0,favorite INTEGER NOT NULL DEFAULT 0,lastplayed TEXT);
 CREATE TABLE IF NOT EXISTS tokens(token TEXT PRIMARY KEY,expires INTEGER);
 CREATE TABLE IF NOT EXISTS prefs(id TEXT PRIMARY KEY,body TEXT);
 CREATE TABLE IF NOT EXISTS index_state(id TEXT PRIMARY KEY,body TEXT);
 CREATE TABLE IF NOT EXISTS pcmetadata(relpath TEXT PRIMARY KEY,size INTEGER,ticks INTEGER,streams TEXT);` + nodeSchema)
	if e != nil {
		db.Close()
		return nil, e
	}
	s := &Server{sharedState: &sharedState{
		cfg:  cfg,
		db:   db,
		cred: c,
	}}
	if err := s.initializeServerIdentity(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.initializeAccounts(); err != nil {
		db.Close()
		return nil, err
	}
	var syncTime string
	if db.QueryRow("SELECT body FROM index_state WHERE id='last_sync'").Scan(&syncTime) == nil {
		s.lastSync, _ = time.Parse(time.RFC3339, syncTime)
	}
	s.httpClient = &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   4,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	if cfg.MetadataFile != "" {
		if e = s.importMetadata(cfg.MetadataFile); e != nil {
			db.Close()
			return nil, e
		}
	}
	return s, nil
}

func (s *Server) importMetadata(file string) error {
	f, e := os.Open(file)
	if e != nil {
		return e
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 65536), 2<<20)
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for sc.Scan() {
		var v struct {
			RelPath     string
			Size, Ticks int64
			Streams     []M
		}
		if e = json.Unmarshal(sc.Bytes(), &v); e != nil {
			return e
		}
		if v.RelPath == "" || v.Size <= 0 {
			return errors.New("invalid PC metadata entry")
		}
		b, _ := json.Marshal(v.Streams)
		if _, e = tx.Exec("INSERT OR REPLACE INTO pcmetadata VALUES(?,?,?,?)", strings.ToLower(strings.ReplaceAll(v.RelPath, `\`, "/")), v.Size, v.Ticks, string(b)); e != nil {
			return e
		}
	}
	if e = sc.Err(); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if info, err := os.Stat(file); err == nil {
		s.metadataStamp = info.ModTime()
	}
	return nil
}

func (s *Server) media(id string) (Media, error) {
	var m Media
	var b string
	e := s.db.QueryRow("SELECT body FROM nodes WHERE id=?", id).Scan(&b)
	if e != nil {
		return m, e
	}
	e = json.Unmarshal([]byte(b), &m)
	if e == nil && !s.permitsRoot(m.RootKey) {
		return m, sql.ErrNoRows
	}
	if e != nil {
		return m, e
	}
	if m.Kind == "Video" {
		var streams string
		var ticks, size int64
		if s.db.QueryRow("SELECT size,ticks,streams FROM pcmetadata WHERE relpath=?", strings.ToLower(m.RelPath)).Scan(&size, &ticks, &streams) == nil && size == m.Size {
			json.Unmarshal([]byte(streams), &m.Streams)
			m.Ticks = ticks
		}
	}
	if len(m.Streams) == 0 {
		m.Streams = []M{}
		if m.Kind == "Video" {
			m.Streams = append(m.Streams, M{
				"Index":     0,
				"Type":      "Video",
				"Width":     m.Width,
				"Height":    m.Height,
				"IsDefault": true,
			})
		}
		if m.Kind == "Audio" {
			codec := map[string]string{
				"mp3":  "mp3",
				"flac": "flac",
				"m4a":  "aac",
				"ogg":  "vorbis",
				"wav":  "pcm_s16le",
			}[m.Container]
			m.Streams = append(m.Streams, M{
				"Index":      0,
				"Type":       "Audio",
				"Codec":      codec,
				"Channels":   m.Channels,
				"SampleRate": m.SampleRate,
				"BitRate":    m.BitRate,
				"IsDefault":  true,
			})
		}
	}
	for _, st := range m.Streams {
		describeStream(st)
	}
	if (m.Kind == "Folder" || m.Kind == "CollectionFolder") && m.CoverURL == "" {
		var cover string
		if s.db.QueryRow("SELECT n.body FROM closure c JOIN nodes n ON n.id=c.descendant WHERE c.ancestor=? AND n.kind NOT IN ('Folder','CollectionFolder') AND json_extract(n.body,'$.CoverURL')<>'' ORDER BY n.sortname LIMIT 1", id).Scan(&cover) == nil {
			var child Media
			if json.Unmarshal([]byte(cover), &child) == nil {
				m.CoverURL = child.CoverURL
				m.SmallCoverURL = child.SmallCoverURL
			}
		}
	}
	return m, nil
}

func (s *Server) imageTag(m Media) string {
	if m.CoverURL == "" && m.Kind != "Photo" {
		return ""
	}
	h := hmac.New(sha256.New, []byte(s.profileSigningKey()))
	fmt.Fprint(h, "profile-image-v1", s.profileID(), "\x00", m.ID, "\x00", m.CoverURL, "\x00", m.SourceURL)
	return s.profileID() + "." + hex.EncodeToString(h.Sum(nil)[:16])
}

func (s *Server) validImageTag(id, tag string) bool {
	_, valid := s.signedImageView(id, tag)
	return valid
}

func (s *Server) signedImageView(id, tag string) (*Server, bool) {
	parts := strings.SplitN(tag, ".", 2)
	accountID := userID
	if len(parts) == 2 {
		accountID = parts[0]
	} else if len(tag) != 32 {
		return nil, false
	}
	view, valid := s.accountView(accountID)
	if !valid {
		return nil, false
	}
	media, err := view.media(id)
	if err != nil {
		return nil, false
	}
	expected := view.imageTag(media)
	if len(parts) == 1 {
		if view.identity.Hash != s.cred.Hash {
			return nil, false
		}
		// Preserve existing owner image URLs, but still check the account's
		// current enabled state and library permissions on every request.
		hash := hmac.New(sha256.New, []byte(s.cred.Hash))
		fmt.Fprint(hash, "photo-sizing-v3-video-cover", media.ID, "\x00", media.CoverURL, "\x00", media.SourceURL)
		expected = hex.EncodeToString(hash.Sum(nil)[:16])
	}
	return view, subtle.ConstantTimeCompare([]byte(tag), []byte(expected)) == 1
}

func (s *Server) aggregateFolder() M {
	count := 0
	for _, root := range s.cfg.Roots {
		if s.permitsRoot(root.Key) {
			count++
		}
	}
	return M{
		"Id":                   rootID,
		"Name":                 "Мои папки",
		"Type":                 "Folder",
		"IsFolder":             true,
		"ServerId":             s.serverIdentity(),
		"DisplayPreferencesId": rootID,
		"ChildCount":           count,
		"UserData": M{
			"Key":        rootID,
			"IsFavorite": false,
			"Played":     false,
		},
	}
}

func (s *Server) folder() M {
	m, e := s.media(libraryID)
	if e != nil {
		return M{
			"Id":             libraryID,
			"Name":           "Видео",
			"Type":           "CollectionFolder",
			"IsFolder":       true,
			"CollectionType": "folders",
		}
	}
	return s.item(m)
}

func (s *Server) views(w http.ResponseWriter, r *http.Request) {
	items := []M{}
	for _, root := range s.cfg.Roots {
		if m, e := s.media(rootKeyID(root.Key)); e == nil {
			items = append(items, s.item(m))
		}
	}
	writeJSON(w, 200, M{
		"Items":            items,
		"TotalRecordCount": len(items),
		"StartIndex":       0,
	})
}

func (s *Server) item(m Media) M {
	folder := m.Kind == "Folder" || m.Kind == "CollectionFolder"
	var pos int64
	var played, fav int
	var last sql.NullString
	s.db.QueryRow("SELECT position,played,favorite,lastplayed FROM progress WHERE id=?", s.storageKey(m.ID)).Scan(&pos, &played, &fav, &last)
	ud := M{
		"Key":                   m.ID,
		"PlaybackPositionTicks": pos,
		"Played":                played != 0,
		"IsFavorite":            fav != 0,
		"PlayCount":             0,
		"LastPlayedDate":        last.String,
		"UnplayedItemCount":     0,
	}
	for key, index := range s.rememberedStreams(m) {
		ud[key] = index
	}
	if m.Ticks > 0 {
		ud["PlayedPercentage"] = float64(pos) * 100 / float64(m.Ticks)
	}
	v := M{
		"Id":                   m.ID,
		"Name":                 m.Name,
		"SortName":             m.Name,
		"Type":                 m.Kind,
		"MediaType":            m.MediaType,
		"IsFolder":             folder,
		"ParentId":             m.ParentID,
		"ServerId":             s.serverIdentity(),
		"DisplayPreferencesId": m.ID,
		"CanDelete":            false,
		"CanDownload":          false,
		"UserData":             ud,
		"ImageTags":            M{},
		"BackdropImageTags":    []string{},
		"LocationType":         "FileSystem",
		"Path":                 m.Path,
		"Overview":             "",
		"Genres":               []string{},
		"Studios":              []M{},
		"People":               []M{},
		"ProviderIds":          M{},
	}
	if tag := s.imageTag(m); tag != "" {
		v["ImageTags"] = M{"Primary": tag}
		v["PrimaryImageAspectRatio"] = 16.0 / 9
		if m.Kind == "Audio" {
			v["PrimaryImageAspectRatio"] = 1.0
		}
		if m.Kind == "Photo" && m.Height > 0 {
			v["PrimaryImageAspectRatio"] = float64(m.Width) / float64(m.Height)
		}
	}
	if folder {
		var n int
		s.db.QueryRow("SELECT count(*) FROM nodes WHERE parent=?", m.ID).Scan(&n)
		v["ChildCount"] = n
		v["CollectionType"] = "folders"
		v["MediaType"] = "Unknown"
		if m.RootKey == "photo" {
			v["Type"] = "PhotoAlbum"
		}
		return v
	}
	v["RunTimeTicks"] = m.Ticks
	v["Container"] = m.Container
	v["Size"] = m.Size
	v["MediaStreams"] = m.Streams
	v["MediaSources"] = []M{s.source(m)}
	v["VideoType"] = "VideoFile"
	v["IsHD"] = m.Height >= 720
	v["Width"] = m.Width
	v["Height"] = m.Height
	v["DateCreated"] = m.Modified
	if m.Kind == "Audio" {
		v["Artists"] = []string{m.Artist}
		v["ArtistItems"] = []M{}
		v["Album"] = m.Album
		v["AlbumArtists"] = []M{}
		v["AlbumArtist"] = m.Artist
		v["IndexNumber"] = m.Track
	}
	return v
}

func (s *Server) source(m Media) M {
	audio, subtitle := s.preferredStreams(m)
	saved := s.rememberedStreams(m)
	if index, ok := saved["AudioStreamIndex"]; ok {
		audio = index
	}
	if index, ok := saved["SubtitleStreamIndex"]; ok {
		subtitle = index
	}
	return M{
		"Id":                         m.ID,
		"Name":                       m.Name,
		"Path":                       m.Path,
		"Protocol":                   "File",
		"Type":                       "Default",
		"Container":                  m.Container,
		"Size":                       m.Size,
		"RunTimeTicks":               m.Ticks,
		"MediaStreams":               s.playbackStreams(m),
		"SupportsDirectPlay":         true,
		"SupportsDirectStream":       false,
		"SupportsTranscoding":        false,
		"IsRemote":                   false,
		"IsInfiniteStream":           false,
		"RequiresOpening":            false,
		"RequiresClosing":            false,
		"DefaultAudioStreamIndex":    audio,
		"DefaultSubtitleStreamIndex": subtitle,
		"ReadAtNativeFramerate":      false,
		"ETag":                       stableID(fmt.Sprint(m.ID, m.Size)),
		"Bitrate":                    m.BitRate,
	}
}

func csvList(v string) []string {
	out := []string{}
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, route string) {
	queue := strings.EqualFold(query(r, "NasPlaybackQueue"), "true")
	where := []string{"1=1"}
	args := []any{}
	add := func(q string, v ...any) { where = append(where, q); args = append(args, v...) }
	if s.identity != nil && !s.administrator() {
		allowed := []string{}
		for _, root := range s.cfg.Roots {
			if s.permitsRoot(root.Key) {
				allowed = append(allowed, "?")
				args = append(args, root.Key)
			}
		}
		if len(allowed) == 0 {
			where = append(where, "0=1")
		} else {
			where = append(where, "n.root IN ("+strings.Join(allowed, ",")+")")
		}
	}
	parent := query(r, "ParentId")
	recursive := strings.EqualFold(query(r, "Recursive"), "true")
	search := strings.ToLower(strings.TrimSpace(query(r, "SearchTerm")))
	filters := strings.ToLower(query(r, "Filters"))
	favorite := strings.EqualFold(query(r, "IsFavorite"), "true") || strings.Contains(filters, "isfavorite")
	resume := strings.Contains(route, "resume")
	ids := csvList(query(r, "Ids"))
	if parent != "" {
		if recursive {
			add("n.id IN (SELECT descendant FROM closure WHERE ancestor=? AND depth>0)", parent)
		} else {
			add("n.parent=?", parent)
		}
	} else if len(ids) == 0 {
		if recursive || search != "" || favorite || resume || strings.Contains(route, "latest") {
			add("n.kind NOT IN ('Folder','CollectionFolder')")
		} else {
			add("n.parent=?", rootID)
		}
	}
	if len(ids) > 0 {
		if len(ids) > 500 {
			w.WriteHeader(400)
			return
		}
		q := []string{}
		for _, id := range ids {
			q = append(q, "?")
			args = append(args, id)
		}
		where = append(where, "n.id IN ("+strings.Join(q, ",")+")")
	}
	if search != "" {
		add("instr(n.sortname,?)>0", search)
	}
	if strings.Contains(filters, "isnotfolder") {
		add("n.kind NOT IN ('Folder','CollectionFolder')")
	}
	if favorite {
		add("coalesce(p.favorite,0)=1")
	}
	if resume {
		add("coalesce(p.position,0)>0 AND coalesce(p.played,0)=0")
	}
	if strings.Contains(filters, "isunplayed") {
		add("coalesce(p.played,0)=0")
	} else if strings.Contains(filters, "isplayed") {
		add("coalesce(p.played,0)=1")
	}
	if f := query(r, "IsFolder"); f != "" {
		if f == "true" {
			add("n.kind IN ('Folder','CollectionFolder')")
		} else {
			add("n.kind NOT IN ('Folder','CollectionFolder')")
		}
	}
	types := csvList(query(r, "IncludeItemTypes"))
	if len(types) == 0 {
		types = csvList(query(r, "MediaTypes"))
	}
	if len(types) > 0 {
		if len(types) > 20 {
			w.WriteHeader(400)
			return
		}
		parts := []string{}
		for _, t := range types {
			if t == "PhotoAlbum" {
				parts = append(parts, "(n.root='photo' AND n.kind IN ('Folder','CollectionFolder'))")
			} else {
				parts = append(parts, "n.kind=?")
				args = append(args, t)
			}
		}
		where = append(where, "("+strings.Join(parts, " OR ")+")")
	}

	if queue {
		add("n.kind NOT IN ('Folder','CollectionFolder')")
	}
	args = append([]any{s.storagePrefix()}, args...)
	base := " FROM nodes n LEFT JOIN progress p ON p.id=(? || n.id) WHERE " + strings.Join(where, " AND ")
	var count int
	if e := s.db.QueryRow("SELECT count(*)"+base, args...).Scan(&count); e != nil {
		http.Error(w, "catalog query failed", 500)
		return
	}
	start, _ := strconv.Atoi(query(r, "StartIndex"))
	if start < 0 {
		start = 0
	}
	limit, _ := strconv.Atoi(query(r, "Limit"))
	if limit < 1 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if queue {
		limit = 20000
	}
	order := "CASE WHEN n.kind IN ('Folder','CollectionFolder') THEN 0 ELSE 1 END,n.sortname,n.id"
	if strings.EqualFold(query(r, "SortBy"), "Random") {
		order = "random()"
	} else if strings.Contains(strings.ToLower(query(r, "SortBy")), "dateplayed") || resume {
		order = "p.lastplayed DESC,n.id"
	} else if strings.EqualFold(query(r, "SortOrder"), "Descending") {
		order = "n.sortname DESC,n.id"
	}
	if queue {
		if recursive && !strings.EqualFold(query(r, "SortBy"), "Random") {
			order = "n.relpath,n.id"
		}
		rows, e := s.db.Query("SELECT n.body,coalesce(p.position,0),coalesce(p.played,0),coalesce(p.favorite,0)"+base+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(append([]any{}, args...), limit, start)...)
		if e != nil {
			http.Error(w, "queue unavailable", 500)
			return
		}
		defer rows.Close()
		items := []M{}
		for rows.Next() {
			var raw string
			var pos int64
			var played, fav int
			if e = rows.Scan(&raw, &pos, &played, &fav); e != nil {
				w.WriteHeader(500)
				return
			}
			var m Media
			if e = json.Unmarshal([]byte(raw), &m); e != nil {
				w.WriteHeader(500)
				return
			}
			item := s.queueItem(m, pos, played, fav)
			key := m.Name
			if recursive {
				key = m.RelPath
			}
			item["_QueuePath"] = key
			items = append(items, item)
		}
		if rows.Err() != nil {
			w.WriteHeader(500)
			return
		}
		sortQueue(items, query(r, "SortBy"), strings.EqualFold(query(r, "SortOrder"), "Descending"))
		if seed := query(r, "NasQueueSeed"); seed != "" {
			for i, item := range items {
				if item["Id"] == seed {
					items[0], items[i] = items[i], items[0]
					break
				}
			}
		}
		writeJSON(w, 200, M{
			"Items":            items,
			"TotalRecordCount": count,
			"StartIndex":       start,
		})
		return
	}
	rows, e := s.db.Query("SELECT n.id"+base+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(append([]any{}, args...), limit, start)...)
	if e != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	selected := []string{}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		selected = append(selected, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		w.WriteHeader(500)
		return
	}
	items := []M{}
	for _, id := range selected {
		m, e := s.media(id)
		if e != nil {
			w.WriteHeader(500)
			return
		}
		items = append(items, s.item(m))
	}
	if strings.HasSuffix(route, "/latest") {
		writeJSON(w, 200, items)
		return
	}
	writeJSON(w, 200, M{
		"Items":            items,
		"TotalRecordCount": count,
		"StartIndex":       start,
	})
}

func (s *Server) indexStatus() M {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	counts := M{}
	rows, e := s.db.Query("SELECT root,kind,count(*) FROM nodes GROUP BY root,kind")
	if e == nil {
		for rows.Next() {
			var root, kind string
			var n int
			if err := rows.Scan(&root, &kind, &n); err != nil {
				continue
			}
			if s.permitsRoot(root) {
				previous, _ := counts[kind].(int)
				counts[kind] = previous + n
			}
		}
		rows.Close()
	}
	return M{
		"Version":     "2.0.0",
		"Source":      "Synology ContentDirectory (read-only)",
		"LastSync":    s.lastSync,
		"LastError":   s.lastError,
		"Counts":      counts,
		"Transcoding": false,
	}
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, raw string, isImage bool) bool {
	address, e := s.mediaURL(raw)
	if e != nil {
		http.Error(w, "invalid source URL", 502)
		return false
	}
	// Synology mod_mediaserv retains albumArt=true between requests. Always
	// send an explicit value so a previous cover request cannot poison streams.
	source, _ := url.Parse(address)
	q := source.Query()
	if !q.Has("albumArt") {
		q.Set("albumArt", "false")
	}
	source.RawQuery = q.Encode()
	address = source.String()
	req, e := http.NewRequestWithContext(r.Context(), r.Method, address, nil)
	if e != nil {
		w.WriteHeader(502)
		return false
	}
	req.Header.Set("User-Agent", s.cfg.DLNAUserAgent)
	req.Header.Set("X-User-Agent", s.cfg.DLNAXUserAgent)
	for _, h := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	response, e := s.httpClient.Do(req)
	if e != nil {
		http.Error(w, "Synology media source unavailable", 502)
		return false
	}
	defer response.Body.Close()
	if isImage && response.StatusCode == 404 {
		return false
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 && response.StatusCode != 304 {
		http.Error(w, "unexpected media redirect", 502)
		return true
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := response.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if isImage {
		w.Header().Set("Cache-Control", "private, max-age=3600")
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != "HEAD" {
		io.CopyBuffer(w, response.Body, make([]byte, 32768))
	}
	return true
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, id string) {
	m, e := s.media(id)
	if e != nil || m.SourceURL == "" {
		w.WriteHeader(404)
		return
	}
	s.proxy(w, r, m.SourceURL, false)
}

func (s *Server) image(w http.ResponseWriter, r *http.Request, id string) {
	m, e := s.media(id)
	if e != nil {
		w.WriteHeader(404)
		return
	}
	if m.Kind == "Photo" {
		tracked := &imageResponse{ResponseWriter: w}
		w = tracked
		defer func() {
			sizes := M{}
			for _, key := range []string{"MaxWidth", "Width", "FillWidth", "MaxHeight", "Height", "FillHeight"} {
				if v := query(r, key); v != "" {
					sizes[key] = v
				}
			}
			status := tracked.status
			if status == 0 {
				status = 200
			}
			event := M{
				"Time":          time.Now().UTC().Format(time.RFC3339),
				"Id":            id,
				"Status":        status,
				"Profile":       w.Header().Get("X-NAS-Image-Profile"),
				"Sizes":         sizes,
				"ContentType":   w.Header().Get("Content-Type"),
				"ContentLength": w.Header().Get("Content-Length"),
				"UserAgent":     r.UserAgent(),
			}
			s.statusMu.Lock()
			s.imageDiagnostics = append(s.imageDiagnostics, event)
			if len(s.imageDiagnostics) > 32 {
				s.imageDiagnostics = s.imageDiagnostics[len(s.imageDiagnostics)-32:]
			}
			s.statusMu.Unlock()
		}()
	}
	u := m.CoverURL
	// Jellyfin cards use fillWidth/fillHeight, not only maxWidth/width.
	// Treat any sizing parameter (including zero/fractional values) as a
	// thumbnail request: a hidden card must never fall through to the original.
	sized := false
	for _, key := range []string{"MaxWidth", "Width", "FillWidth", "MaxHeight", "Height", "FillHeight"} {
		if query(r, key) != "" {
			sized = true
			break
		}
	}
	if sized && m.Kind != "Video" && m.RootKey != "video" && m.SmallCoverURL != "" {
		u = m.SmallCoverURL
	}
	role := "cover"
	if m.Kind == "Photo" {
		role = "thumbnail"
		if !sized && m.Container != "heic" && m.Container != "heif" {
			u = m.SourceURL
			role = "original"
		}
		if sized && (u == "" || u == m.SourceURL) {
			w.Header().Set("X-NAS-Image-Profile", "thumbnail")
			w.WriteHeader(404)
			return
		}
	}
	w.Header().Set("X-NAS-Image-Profile", role)

	if u == "" {
		w.WriteHeader(404)
		return
	}
	if !s.proxy(w, r, u, true) {
		if m.SmallCoverURL != "" && m.SmallCoverURL != u && s.proxy(w, r, m.SmallCoverURL, true) {
			return
		}
		w.WriteHeader(404)
	}
}

// Keep the allowlisted host parser referenced here to make endpoint validation
// explicit at startup, rather than waiting for a playback request.
func validateOrigin(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	if u.Scheme != "http" || u.Host == "" || u.User != nil {
		return errors.New("invalid Synology origin")
	}
	return nil
}

// Records response status without buffering or retaining image contents.
type imageResponse struct {
	http.ResponseWriter
	status int
}

func (w *imageResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *imageResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
