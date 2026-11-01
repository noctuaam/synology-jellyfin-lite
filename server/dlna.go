package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const lgAgent = "Linux/4.4.84-p.649.grampians.k5lp.1 UPnP/1.0 LGE WebOS TV LGE_DLNA_SDK/1.6.0/05.40.97 DLNADOC/1.50"
const didlFilter = "res@resolution,res@nrAudioChannels,res@sampleFrequency,res@bitrate,dc:creator,res@dlna:cleartextSize,dc:date,upnp:genre,res,res@duration,res@size,upnp:albumArtURI,upnp:originalTrackNumber,upnp:album,upnp:artist,upnp:author"

func configureDefaults(c *Config) {
	if c.ServerName == "" {
		c.ServerName = "NAS Media Folders"
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8097"
	}
	if c.SynologyControlURL == "" {
		c.SynologyControlURL = "http://" + localLANAddress() + ":50001/ContentDirectory/control"
	}
	if c.SynologyMediaBase == "" {
		host := localLANAddress()
		if control, err := url.Parse(c.SynologyControlURL); err == nil && control.Hostname() != "" {
			host = control.Hostname()
		}
		c.SynologyMediaBase = "http://" + net.JoinHostPort(host, "50002")
	}
	if c.DLNAUserAgent == "" {
		c.DLNAUserAgent = lgAgent
	}
	if c.DLNAXUserAgent == "" {
		c.DLNAXUserAgent = "redsonic"
	}
	if c.DefaultPageSize == 0 {
		c.DefaultPageSize = 100
	}
	if c.RefreshSeconds < 60 {
		c.RefreshSeconds = 900
	}
	if len(c.Roots) == 0 {
		volume := "/volume1"
		if shares, err := filepath.Glob("/volume*/video"); err == nil && len(shares) == 1 {
			volume = filepath.Dir(shares[0])
		}
		c.Roots = []RootSpec{
			{"video", "Видео", "44", path.Join(volume, "video"), "Video"},
			{"music", "Музыка", "22", path.Join(volume, "music"), "Audio"},
			{"photo", "Фото", "38", path.Join(volume, "photo"), "Photo"},
		}
	}
}

func rootKeyID(key string) string {
	if key == "video" {
		return libraryID
	}
	return stableID("synology-root:" + key)
}

type dlnaRes struct {
	URL        string `xml:",chardata"`
	Protocol   string `xml:"protocolInfo,attr"`
	Resolution string `xml:"resolution,attr"`
	Duration   string `xml:"duration,attr"`
	Size       int64  `xml:"size,attr"`
	Channels   int    `xml:"nrAudioChannels,attr"`
	SampleRate int    `xml:"sampleFrequency,attr"`
	BitRate    int    `xml:"bitrate,attr"`
}
type dlnaEntry struct {
	XMLName   xml.Name
	ID        string    `xml:"id,attr"`
	Title     string    `xml:"title"`
	Class     string    `xml:"class"`
	Artist    string    `xml:"artist"`
	Album     string    `xml:"album"`
	Date      string    `xml:"date"`
	Track     int       `xml:"originalTrackNumber"`
	Covers    []string  `xml:"albumArtURI"`
	Resources []dlnaRes `xml:"res"`
}
type didl struct {
	Entries []dlnaEntry `xml:",any"`
}

func (s *Server) soap(ctx context.Context, action, args string) (map[string]string, error) {
	body := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:` + action + ` xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">` + args + `</u:` + action + `></s:Body></s:Envelope>`
	req, e := http.NewRequestWithContext(ctx, "POST", s.cfg.SynologyControlURL, strings.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"urn:schemas-upnp-org:service:ContentDirectory:1#`+action+`"`)
	req.Header.Set("User-Agent", s.cfg.DLNAUserAgent)
	req.Header.Set("X-User-Agent", s.cfg.DLNAXUserAgent)
	response, e := s.httpClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Synology %s: HTTP %d", action, response.StatusCode)
	}
	d := xml.NewDecoder(io.LimitReader(response.Body, 16<<20))
	fields := map[string]string{}
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if st, ok := t.(xml.StartElement); ok {
			switch st.Name.Local {
			case "Result", "TotalMatches", "NumberReturned", "Id", "UpdateID":
				var text string
				if e = d.DecodeElement(&text, &st); e != nil {
					return nil, e
				}
				fields[st.Name.Local] = text
			}
		}
	}
	return fields, nil
}

func escapeXML(v string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(v))
	return b.String()
}

func (s *Server) browse(ctx context.Context, id string, start int) ([]dlnaEntry, int, error) {
	args := fmt.Sprintf("<ObjectID>%s</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag><Filter>%s</Filter><StartingIndex>%d</StartingIndex><RequestedCount>200</RequestedCount><SortCriteria></SortCriteria>", escapeXML(id), didlFilter, start)
	f, e := s.soap(ctx, "Browse", args)
	if e != nil {
		return nil, 0, e
	}
	var doc didl
	if e = xml.Unmarshal([]byte(f["Result"]), &doc); e != nil {
		return nil, 0, e
	}
	n, e := strconv.Atoi(f["TotalMatches"])
	if e != nil || n < 0 {
		return nil, 0, errors.New("invalid DLNA TotalMatches")
	}
	return doc.Entries, n, nil
}

func durationTicks(v string) int64 {
	p := strings.Split(v, ":")
	if len(p) != 3 {
		return 0
	}
	h, _ := strconv.ParseFloat(p[0], 64)
	m, _ := strconv.ParseFloat(p[1], 64)
	sec, _ := strconv.ParseFloat(p[2], 64)
	return int64((h*3600 + m*60 + sec) * 1e7)
}

// Only the configured Synology media origin is trusted. Hostnames in DIDL may
// use the NAS LAN address; the control endpoint hostname is trusted as well.
func (s *Server) mediaURL(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil {
		return "", e
	}
	base, e := url.Parse(s.cfg.SynologyMediaBase)
	if e != nil {
		return "", e
	}
	control, _ := url.Parse(s.cfg.SynologyControlURL)
	if u.Scheme != "http" || u.User != nil || u.Fragment != "" {
		return "", errors.New("untrusted media URL")
	}
	host := u.Hostname()
	public, _ := url.Parse(s.cfg.PublicURL)
	if host != base.Hostname() && host != control.Hostname() && (public == nil || host != public.Hostname()) {
		return "", errors.New("foreign media host")
	}
	if u.Port() != base.Port() {
		return "", errors.New("foreign media port")
	}
	u.Scheme = base.Scheme
	u.Host = base.Host
	return u.String(), nil
}

func (s *Server) entryMedia(e dlnaEntry, root RootSpec, parent, rel string) (Media, error) {
	m := Media{
		Name:     e.Title,
		SourceID: e.ID,
		ParentID: parent,
		RootKey:  root.Key,
		Kind:     "Folder",
		RelPath:  path.Join(rel, e.Title),
		Artist:   e.Artist,
		Album:    e.Album,
		Track:    e.Track,
		Modified: e.Date,
	}
	if e.ID == "" || (e.XMLName.Local == "container" && (strings.Contains(e.Title, "/") || e.Title == ".." || e.Title == ".")) {
		return m, errors.New("unsafe DLNA entry name or ID")
	}
	m.ID = stableID("folder:" + root.Key + ":" + m.RelPath)
	if e.XMLName.Local == "container" {
		return m, nil
	}
	switch {
	case strings.Contains(e.Class, "videoItem"):
		m.Kind = "Video"
	case strings.Contains(e.Class, "audioItem"):
		m.Kind = "Audio"
	case strings.Contains(e.Class, "imageItem"):
		m.Kind = "Photo"
	default:
		return m, errors.New("unsupported DLNA class")
	}
	m.MediaType = m.Kind
	var selected dlnaRes
	score := -1
	for _, r := range e.Resources {
		p := strings.Split(r.Protocol, ":")
		if len(p) < 3 {
			continue
		}
		mime := p[2]
		good := strings.HasPrefix(mime, "video/") && m.Kind == "Video" || strings.HasPrefix(mime, "audio/") && m.Kind == "Audio" || strings.HasPrefix(mime, "image/") && m.Kind == "Photo"
		if mime == "application/octet-stream" {
			good = true
		}
		if !good {
			continue
		}
		w, h := 0, 0
		fmt.Sscanf(r.Resolution, "%dx%d", &w, &h)
		v := w * h
		if strings.Contains(r.URL, "/NDLNA/") {
			v += 100000000
		}
		if v > score {
			selected = r
			score = v
		}
	}
	if score < 0 {
		return m, errors.New("no original resource")
	}
	var err error
	m.SourceURL, err = s.mediaURL(selected.URL)
	if err != nil {
		return m, err
	}
	m.Container = strings.ToLower(strings.TrimPrefix(path.Ext(strings.Split(selected.URL, "?")[0]), "."))
	m.Size = selected.Size
	m.Ticks = durationTicks(selected.Duration)
	m.Channels = selected.Channels
	m.SampleRate = selected.SampleRate
	m.BitRate = selected.BitRate * 8
	fmt.Sscanf(selected.Resolution, "%dx%d", &m.Width, &m.Height)
	m.ID = stableID("item:" + root.Key + ":" + e.ID)
	if m.Kind == "Video" {
		m.RelPath += "." + m.Container
		m.Path = path.Join(root.LocalPath, m.RelPath)
		m.ID = stableID(m.Path)
	}
	if len(e.Covers) > 0 {
		m.SmallCoverURL, err = s.mediaURL(e.Covers[0])
		if err != nil {
			return m, err
		}
		m.CoverURL = m.SmallCoverURL
	}
	if m.Kind == "Photo" && m.CoverURL == "" {
		for _, r := range e.Resources {
			if strings.Contains(r.Protocol, "JPEG_TN") {
				m.CoverURL, _ = s.mediaURL(r.URL)
				break
			}
		}
		if m.CoverURL == "" {
			u, _ := url.Parse(m.SourceURL)
			u.Path = "/sp/JPEG_TN/" + strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path)) + ".jpg"
			u.RawQuery = ""
			m.CoverURL = u.String()
		}
		m.SmallCoverURL = m.CoverURL
	}
	if m.Kind == "Video" && m.CoverURL != "" {
		u, _ := url.Parse(m.SourceURL)
		q := u.Query()
		q.Set("albumArt", "true")
		u.RawQuery = q.Encode()
		m.CoverURL = u.String()
	}
	return m, nil
}

func (s *Server) refreshLoop(ctx context.Context) {
	s.statusMu.RLock()
	first := s.lastSync.IsZero()
	s.statusMu.RUnlock()
	if first {
		if e := s.syncIndex(ctx, false); e != nil {
			log.Printf("index refresh: %v", e)
		}
	}
	t := time.NewTicker(time.Duration(s.cfg.RefreshSeconds) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if e := s.syncIndex(ctx, false); e != nil {
				log.Printf("index refresh: %v", e)
			}
		}
	}
}

func (s *Server) syncIndex(ctx context.Context, force bool) (err error) {
	if !s.scan.TryLock() {
		return errors.New("index sync already running")
	}
	defer s.scan.Unlock()
	defer func() {
		s.statusMu.Lock()
		defer s.statusMu.Unlock()
		if err != nil {
			s.lastError = err.Error()
		} else {
			s.lastError = ""
			s.lastSync = time.Now()
		}
	}()
	if s.cfg.MetadataFile != "" {
		info, metadataErr := os.Stat(s.cfg.MetadataFile)
		if metadataErr != nil {
			return metadataErr
		}
		if !info.ModTime().Equal(s.metadataStamp) {
			if metadataErr = s.importMetadata(s.cfg.MetadataFile); metadataErr != nil {
				return metadataErr
			}
		}
	}
	update := ""
	if f, e := s.soap(ctx, "GetSystemUpdateID", ""); e == nil {
		update = f["Id"]
	}
	// Synology may report a constant zero UpdateID. Refresh even when unchanged.
	// Keep only hashes of existing rows in RAM. Unchanged refreshes perform no
	// catalog rewrites; this avoids tens of megabytes of HDD writes every poll.
	previous := map[string][32]byte{}
	existing, e := s.db.Query("SELECT id,body FROM nodes")
	if e != nil {
		return e
	}
	for existing.Next() {
		var id, body string
		if e = existing.Scan(&id, &body); e != nil {
			existing.Close()
			return e
		}
		previous[id] = sha256.Sum256([]byte(body))
	}
	e = existing.Err()
	existing.Close()
	if e != nil {
		return e
	}
	changed := 0
	f, e := os.CreateTemp(s.cfg.Data, "index-stage-*.db")
	if e != nil {
		return e
	}
	stage := f.Name()
	f.Close()
	defer os.Remove(stage)
	db, e := sql.Open("sqlite", stage)
	if e != nil {
		return e
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec("PRAGMA journal_mode=OFF; PRAGMA cache_size=-2048;" + nodeSchema); e != nil {
		return e
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	insert, e := tx.Prepare("INSERT INTO nodes(id,parent,root,kind,name,sortname,sourceid,relpath,body) VALUES(?,?,?,?,?,?,?,?,?)")
	if e != nil {
		return e
	}
	defer insert.Close()
	add := func(m Media, anc []string) error {
		b, _ := json.Marshal(m)
		old, found := previous[m.ID]
		delete(previous, m.ID)
		if found && old == sha256.Sum256(b) {
			return nil
		}
		changed++
		if _, e := insert.Exec(m.ID, m.ParentID, m.RootKey, m.Kind, m.Name, strings.ToLower(m.Name), m.SourceID, m.RelPath, string(b)); e != nil {
			return e
		}
		for i, a := range anc {
			if _, e := tx.Exec("INSERT INTO closure VALUES(?,?,?)", a, m.ID, len(anc)-i); e != nil {
				return e
			}
		}
		_, e := tx.Exec("INSERT INTO closure VALUES(?,?,0)", m.ID, m.ID)
		return e
	}
	type folderJob struct {
		root            RootSpec
		id, parent, rel string
		anc             []string
	}
	queue := []folderJob{}
	for _, root := range s.cfg.Roots {
		m := Media{
			ID:       rootKeyID(root.Key),
			ParentID: rootID,
			RootKey:  root.Key,
			Kind:     "CollectionFolder",
			Name:     root.Name,
			SourceID: root.ObjectID,
		}
		if e = add(m, []string{rootID}); e != nil {
			return e
		}
		queue = append(queue, folderJob{root, root.ObjectID, m.ID, "", []string{rootID, m.ID}})
	}
	seen := map[string]bool{}
	count := 0
	for len(queue) > 0 {
		job := queue[0]
		queue = queue[1:]
		key := job.root.Key + ":" + job.id
		if seen[key] {
			return errors.New("cycle in DLNA folder index")
		}
		seen[key] = true
		if len(seen)%100 == 0 {
			log.Printf("Import progress: %d folders, %d items", len(seen), count)
		}
		if len(seen) > 20000 {
			return errors.New("too many source folders")
		}
		start := 0
		for {
			entries, total, e := s.browse(ctx, job.id, start)
			if e != nil {
				return fmt.Errorf("browse %s: %w", job.id, e)
			}
			if len(entries) == 0 && start < total {
				return errors.New("truncated DLNA page")
			}
			for _, entry := range entries {
				m, e := s.entryMedia(entry, job.root, job.parent, job.rel)
				if e != nil {
					return fmt.Errorf("source item %s: %w", entry.ID, e)
				}
				if e = add(m, job.anc); e != nil {
					return e
				}
				count++
				if count > 100000 {
					return errors.New("source item limit exceeded")
				}
				if m.Kind == "Folder" {
					anc := append(append([]string{}, job.anc...), m.ID)
					queue = append(queue, folderJob{job.root, entry.ID, m.ID, m.RelPath, anc})
				}
			}
			start += len(entries)
			if start >= total {
				break
			}
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	insert.Close()
	db.Close()
	// The staging database makes publication atomic: failed or interrupted syncs
	// leave the previous complete catalog and all user state intact.
	s.db.SetMaxOpenConns(1)
	if _, e = s.db.Exec("ATTACH DATABASE ? AS incoming", filepath.ToSlash(stage)); e != nil {
		return e
	}
	defer s.db.Exec("DETACH DATABASE incoming")
	publish, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer publish.Rollback()
	for id := range previous {
		if _, e = publish.Exec("DELETE FROM nodes WHERE id=?", id); e != nil {
			return e
		}
		if _, e = publish.Exec("DELETE FROM closure WHERE descendant=? OR ancestor=?", id, id); e != nil {
			return e
		}
	}
	if changed > 0 {
		if _, e = publish.Exec("DELETE FROM closure WHERE descendant IN (SELECT id FROM incoming.nodes); DELETE FROM nodes WHERE id IN (SELECT id FROM incoming.nodes); INSERT INTO nodes SELECT * FROM incoming.nodes; INSERT OR REPLACE INTO closure SELECT * FROM incoming.closure;"); e != nil {
			return e
		}
	}
	if _, e = publish.Exec("INSERT OR REPLACE INTO index_state VALUES('last_sync',?)", time.Now().UTC().Format(time.RFC3339)); e != nil {
		return e
	}
	if e = publish.Commit(); e != nil {
		return e
	}

	s.sourceUpdateID = update
	log.Printf("Synology folder index imported: %d items, %d folders; no media probing", count, len(seen))
	log.Printf("Index delta: %d changed, %d removed", changed, len(previous))
	return nil
}
