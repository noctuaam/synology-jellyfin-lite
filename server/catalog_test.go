package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Server, *atomic.Bool) {
	t.Helper()
	bad := &atomic.Bool{}
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/control" {
			if bad.Load() {
				http.Error(w, "unavailable", 503)
				return
			}
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), "GetSystemUpdateID") {
				fmt.Fprint(w, `<Envelope><Id>0</Id></Envelope>`)
				return
			}
			if !strings.Contains(r.Header.Get("User-Agent"), "LGE") {
				t.Error("missing Synology LG profile")
			}
			var entries string
			switch {
			case strings.Contains(string(raw), "<ObjectID>44</ObjectID>"):
				entries = `<container id="vf"><title>Сериалы</title></container><item id="44$@1"><title>Фильм</title><class>object.item.videoItem</class><albumArtURI>` + upstream.URL + `/cover.jpg</albumArtURI><res protocolInfo="http-get:*:video/x-matroska:*" size="100" duration="00:02:00" resolution="1920x1080">` + upstream.URL + `/v/NDLNA/1.mkv</res></item>`
			case strings.Contains(string(raw), "<ObjectID>vf</ObjectID>"):
				entries = `<item id="44$@2"><title>Серия</title><class>object.item.videoItem</class><res protocolInfo="http-get:*:video/mp4:*" size="10">` + upstream.URL + `/v/NDLNA/2.mp4</res></item>`
			case strings.Contains(string(raw), "<ObjectID>22</ObjectID>"):
				entries = `<item id="22$@3"><title>Artist / Track</title><class>object.item.audioItem.musicTrack</class><artist>Исполнитель</artist><album>Альбом</album><res protocolInfo="http-get:*:audio/mpeg:*" duration="00:03:10" size="10" nrAudioChannels="2" sampleFrequency="44100">` + upstream.URL + `/a/NDLNA/3.mp3</res></item>`
			case strings.Contains(string(raw), "<ObjectID>38</ObjectID>"):
				entries = `<item id="38$@4"><title>Фото</title><class>object.item.imageItem.photo</class><albumArtURI>` + upstream.URL + `/thumb.jpg</albumArtURI><res protocolInfo="http-get:*:image/jpeg:JPEG_LRG" size="10" resolution="4000x3000">` + upstream.URL + `/p/NDLNA/4.jpg</res></item>`
			default:
				t.Errorf("unexpected browse: %s", raw)
			}
			n := strings.Count(entries, "<item ") + strings.Count(entries, "<container ")
			fmt.Fprintf(w, "<Envelope><Result>%s</Result><TotalMatches>%d</TotalMatches></Envelope>", escapeXML(`<DIDL-Lite>`+entries+`</DIDL-Lite>`), n)
			return
		}
		if r.URL.Path == "/a/NDLNA/3.mp3" && r.URL.Query().Get("albumArt") != "false" {
			http.Error(w, "sticky cover state", 404)
			return
		}
		if r.Header.Get("X-Emby-Token") != "" || r.Header.Get("Authorization") != "" {
			t.Error("client credentials leaked upstream")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, "test", time.Unix(1700000000, 0), strings.NewReader("0123456789"))
	}))
	t.Cleanup(upstream.Close)
	cfg := Config{
		ServerID:           serverID,
		DefaultPageSize:    1000,
		Data:               t.TempDir(),
		PublicURL:          upstream.URL,
		SynologyControlURL: upstream.URL + "/control",
		SynologyMediaBase:  upstream.URL,
	}
	s, e := openServer(cfg, Credentials{"viewer", "salt", passwordHash("123", "salt")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.db.Close() })
	if e = s.syncIndex(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	s.db.Exec("INSERT INTO tokens VALUES('test',?)", time.Now().Add(time.Hour).Unix())
	return s, bad
}

func call(s *Server, method, path, body string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		r.Header.Set("X-Emby-Token", "test")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func jsonBody(t *testing.T, w *httptest.ResponseRecorder) M {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var v M
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	return v
}

func TestFolderCatalogAndLegacyState(t *testing.T) {
	s, _ := fixture(t)
	v := jsonBody(t, call(s, "GET", "/UserViews", "", true))
	if len(v["Items"].([]any)) != 3 {
		t.Fatal(v)
	}
	v = jsonBody(t, call(s, "GET", "/Items?ParentId="+libraryID, "", true))
	if int(v["TotalRecordCount"].(float64)) != 2 {
		t.Fatal(v)
	}
	items := v["Items"].([]any)
	folder := items[0].(map[string]any)
	if folder["Name"] != "Сериалы" || folder["CollectionType"] != "folders" {
		t.Fatal(folder)
	}
	v = jsonBody(t, call(s, "GET", "/Items?ParentId="+folder["Id"].(string), "", true))
	if len(v["Items"].([]any)) != 1 {
		t.Fatal(v)
	}
	id := stableID("/volume1/video/Фильм.mkv")
	if _, e := s.media(id); e != nil {
		t.Fatal("legacy ID changed", e)
	}
	if w := call(s, "POST", "/UserFavoriteItems/"+id, "", true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call(s, "POST", "/Sessions/Playing/Progress", `{"ItemId":"`+id+`","PositionTicks":100}`, true); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if e := s.syncIndex(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	v = jsonBody(t, call(s, "GET", "/Items?Recursive=true&Filters=IsFavorite", "", true))
	if len(v["Items"].([]any)) != 1 {
		t.Fatal("favorites lost", v)
	}
	m, _ := s.media(id)
	if s.item(m)["UserData"].(M)["PlaybackPositionTicks"] != int64(100) {
		t.Fatal("progress lost")
	}
}

func TestAtomicRefreshFailureAndConstantUpdateID(t *testing.T) {
	s, bad := fixture(t)
	bad.Store(true)
	if e := s.syncIndex(context.Background(), true); e == nil {
		t.Fatal("expected failure")
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM nodes").Scan(&n)
	if n != 8 {
		t.Fatal("old catalog damaged", n)
	}
	bad.Store(false)
	s.db.Exec("DELETE FROM nodes WHERE kind='Video'")
	if e := s.syncIndex(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	s.db.QueryRow("SELECT count(*) FROM nodes WHERE kind='Video'").Scan(&n)
	if n != 2 {
		t.Fatal("constant update ID prevented discovery", n)
	}
}

func TestSignedImagesAndMediaRange(t *testing.T) {
	s, _ := fixture(t)
	id := stableID("/volume1/video/Фильм.mkv")
	m, _ := s.media(id)
	if w := call(s, "GET", "/Items/"+id+"/Images/Primary", "", false); w.Code != 401 {
		t.Fatal("unsigned image exposed", w.Code)
	}
	if w := call(s, "GET", "/Items/"+id+"/Images/Primary?tag="+s.imageTag(m), "", false); w.Code != 200 {
		t.Fatal("client image failed", w.Code)
	}
	if w := call(s, "GET", "/Videos/"+id+"/stream.mkv", "", false); w.Code != 401 {
		t.Fatal("stream exposed")
	}
	r := httptest.NewRequest("GET", "/Videos/"+id+"/stream.mkv", nil)
	r.Header.Set("X-Emby-Token", "test")
	r.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "234" || w.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}

func TestSourceURLTrustBoundary(t *testing.T) {
	s, _ := fixture(t)
	for _, raw := range []string{
		"http://example.com:50002/a",
		"file:///etc/passwd",
		"http://user:password@192.0.2.34:50002/a",
		"http://127.0.0.1:22/a",
	} {
		if _, e := s.mediaURL(raw); e == nil {
			t.Error("accepted", raw)
		}
	}
}

func TestFolderHomeAndTypedSearch(t *testing.T) {
	s, _ := fixture(t)
	v := jsonBody(t, call(s, "GET", "/DisplayPreferences/usersettings", "", true))
	p := v["CustomPrefs"].(map[string]any)
	if p["homesection0"] != "smalllibrarytiles" || p["homesection6"] != "none" {
		t.Fatal(p)
	}
	v = jsonBody(t, call(s, "GET", "/Items?Recursive=true&MediaTypes=Audio&SearchTerm="+url.QueryEscape("artist"), "", true))
	if len(v["Items"].([]any)) != 1 {
		t.Fatal(v)
	}
	v = jsonBody(t, call(s, "GET", "/Items?Recursive=true&Limit=1&StartIndex=2", "", true))
	if len(v["Items"].([]any)) != 1 || v["TotalRecordCount"] != float64(4) {
		t.Fatal(v)
	}
}

func TestMetadataSizeValidation(t *testing.T) {
	s, _ := fixture(t)
	s.db.Exec("INSERT INTO pcmetadata VALUES(?,?,?,?)", strings.ToLower("Фильм.mkv"), 100, 500, `[{"Index":0,"Type":"Video","Codec":"hevc"},{"Index":1,"Type":"Audio","Codec":"ac3","Language":"rus","Channels":6}]`)
	m, e := s.media(stableID("/volume1/video/Фильм.mkv"))
	if e != nil || len(m.Streams) != 2 || m.Ticks != 500 {
		t.Fatal(m, e)
	}
	if !strings.Contains(m.Streams[1]["DisplayTitle"].(string), "Русский") {
		t.Fatal(m.Streams)
	}
	s.db.Exec("UPDATE pcmetadata SET size=99")
	m, _ = s.media(m.ID)
	if len(m.Streams) != 1 || m.Ticks == 500 {
		t.Fatal("stale metadata used")
	}
	if e = s.importMetadata(filepath.Join(t.TempDir(), "missing")); e == nil {
		t.Fatal("missing metadata silently ignored")
	}
}

func TestAudioUniversalAndPhotoAlbum(t *testing.T) {
	s, _ := fixture(t)
	v := jsonBody(t, call(s, "GET", "/Items?Recursive=true&MediaTypes=Audio", "", true))
	id := v["Items"].([]any)[0].(map[string]any)["Id"].(string)
	if w := call(s, "GET", "/Audio/"+id+"/universal", "", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v = jsonBody(t, call(s, "GET", "/Items/"+rootKeyID("photo"), "", true))
	if v["Type"] != "PhotoAlbum" {
		t.Fatal("client cannot start a folder slideshow", v)
	}
}

func TestClientReconnectionTokenHeader(t *testing.T) {
	s, _ := fixture(t)
	r := httptest.NewRequest("GET", "/System/Info", nil)
	r.Header.Set("X-MediaBrowser-Token", "test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("official client discards saved login on reload", w.Code)
	}
}

func TestMemoryProfileKeepsClientPaginationConsistent(t *testing.T) {
	s, _ := fixture(t)
	s.cfg.Web = t.TempDir()
	os.WriteFile(filepath.Join(s.cfg.Web, "index.html"), []byte("<html><head></head><body></body></html>"), 0600)
	w := call(s, "GET", "/web/index.html", "", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), userID+`-libraryPageSize","1000"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}

type recordTransport struct {
	base http.RoundTripper
	urls []string
}

func (t *recordTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.urls = append(t.urls, r.URL.String())
	return t.base.RoundTrip(r)
}

func TestGridUsesExistingSmallCoverAndFullPhotoStillAvailable(t *testing.T) {
	s, _ := fixture(t)
	rec := &recordTransport{base: s.httpClient.Transport}
	s.httpClient.Transport = rec
	id := stableID("/volume1/video/Фильм.mkv")
	m, _ := s.media(id)
	w := call(s, "GET", "/Items/"+id+"/Images/Primary?maxWidth=900&tag="+s.imageTag(m), "", false)
	if w.Code != 200 || !strings.Contains(rec.urls[len(rec.urls)-1], "/v/NDLNA/1.mkv?albumArt=true") {
		t.Fatal(w.Code, rec.urls)
	}
	var photo string
	s.db.QueryRow("SELECT id FROM nodes WHERE kind='Photo'").Scan(&photo)
	m, _ = s.media(photo)
	w = call(s, "GET", "/Items/"+photo+"/Images/Primary?maxWidth=900&tag="+s.imageTag(m), "", false)
	if w.Code != 200 || !strings.Contains(rec.urls[len(rec.urls)-1], "/thumb.jpg") {
		t.Fatal(w.Code, rec.urls)
	}
	w = call(s, "GET", "/Items/"+photo+"/Images/Primary?tag="+s.imageTag(m), "", false)
	if w.Code != 200 || !strings.Contains(rec.urls[len(rec.urls)-1], "/p/NDLNA/4.jpg") {
		t.Fatal(w.Code, rec.urls)
	}
}

func TestWholeFolderQueueIsSeparateFromGridPages(t *testing.T) {
	s, _ := fixture(t)
	tx, e := s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 1010; i++ {
		m := Media{
			ID:        fmt.Sprintf("extra%04d", i),
			Name:      fmt.Sprintf("Track %04d", i),
			ParentID:  rootKeyID("music"),
			RootKey:   "music",
			Kind:      "Audio",
			MediaType: "Audio",
			Ticks:     1000,
		}
		b, _ := json.Marshal(m)
		if _, e = tx.Exec("INSERT INTO nodes VALUES(?,?,?,?,?,?,?,?,?)", m.ID, m.ParentID, m.RootKey, m.Kind, m.Name, m.Name, m.ID, m.Name, string(b)); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	base := "/Items?ParentId=" + rootKeyID("music") + "&MediaTypes=Audio&Limit=24"
	v := jsonBody(t, call(s, "GET", base, "", true))
	if len(v["Items"].([]any)) != 24 || v["TotalRecordCount"] != float64(1011) {
		t.Fatal(v["TotalRecordCount"])
	}
	page := jsonBody(t, call(s, "GET", "/Items?ParentId="+rootKeyID("music")+"&MediaTypes=Audio&Limit=1000", "", true))
	if len(page["Items"].([]any)) != 1000 {
		t.Fatal("page truncated", len(page["Items"].([]any)))
	}
	next := jsonBody(t, call(s, "GET", "/Items?ParentId="+rootKeyID("music")+"&MediaTypes=Audio&Limit=1000&StartIndex=1000", "", true))
	if len(next["Items"].([]any)) != 11 {
		t.Fatal("next page skips files", len(next["Items"].([]any)))
	}
	v = jsonBody(t, call(s, "GET", base+"&NasPlaybackQueue=true&SortBy=Random", "", true))
	items := v["Items"].([]any)
	if len(items) != 1011 {
		t.Fatal("queue was truncated", len(items))
	}
	seen := map[string]bool{}
	for _, item := range items {
		m := item.(map[string]any)
		id := m["Id"].(string)
		if seen[id] {
			t.Fatal("duplicate queue item")
		}
		seen[id] = true
		if _, exists := m["MediaSources"]; exists {
			t.Fatal("large playback metadata embedded in queue")
		}
		if _, exists := m["MediaStreams"]; exists {
			t.Fatal("stream metadata embedded in queue")
		}
	}
}

func TestVideoAutoAdvanceSettingPersists(t *testing.T) {
	s, _ := fixture(t)
	if s.user()["Configuration"].(M)["EnableNextEpisodeAutoPlay"] != true {
		t.Fatal("video queue cannot advance")
	}
	w := call(s, "POST", "/Users/"+userID+"/Configuration", `{"EnableNextEpisodeAutoPlay":false}`, true)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if s.user()["Configuration"].(M)["EnableNextEpisodeAutoPlay"] != false {
		t.Fatal("autoplay preference was not saved")
	}
}

func TestPinnedOfficialClientQueueChanges(t *testing.T) {
	base := filepath.Join("..", "web-dist")
	b, e := os.ReadFile(filepath.Join(base, "main.jellyfin.bundle.js"))
	if os.IsNotExist(e) {
		t.Skip("official client fixture not present")
	}
	if e != nil {
		t.Fatal(e)
	}
	patched, e := patchQueueClient(b)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(patched), `NasPlaybackQueue="true"`) || !strings.Contains(string(patched), `("Audio"===e.MediaType||"Video"===e.MediaType)&&(!a||1===a.length)`) {
		t.Fatal("queue patch incomplete")
	}
	if _, e = patchQueueClient([]byte("different client")); e == nil {
		t.Fatal("unrecognized client was patched")
	}
	for _, name := range []string{"runtime.bundle.js", "27884.448518055bdb8b476d31.chunk.js"} {
		b, e = os.ReadFile(filepath.Join(base, name))
		if e != nil {
			t.Fatal(e)
		}
		patched, e = patchQueueAsset(name, b)
		if e != nil {
			t.Fatal(e)
		}
		if name == "runtime.bundle.js" && !strings.Contains(string(patched), "chunk.js?nasQueue=200") {
			t.Fatal("old client cache not bypassed")
		}
		if name != "runtime.bundle.js" && !strings.Contains(string(patched), "virtual:{slides:h,cache:!1") {
			t.Fatal("slideshow retains every visited image")
		}
	}
}

func TestInstantMixUsesOnlyTheSelectedTracksFolder(t *testing.T) {
	s, _ := fixture(t)
	var id string
	s.db.QueryRow("SELECT id FROM nodes WHERE kind='Audio'").Scan(&id)
	v := jsonBody(t, call(s, "GET", "/Items/"+id+"/InstantMix?Limit=200", "", true))
	items := v["Items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["Id"] != id {
		t.Fatal(v)
	}
}

func TestSequentialQueueSortsEpisodeNumbers(t *testing.T) {
	items := []M{
		{"_QueuePath": "Season 1/Episode 10.mkv"},
		{"_QueuePath": "Season 1/Episode 2.mkv"},
		{"_QueuePath": "Season 1/Episode 1.mkv"},
	}
	if !naturalLess("Season 1/Episode 2.mkv", "Season 1/Episode 10.mkv") {
		t.Fatal("episodes sort lexically")
	}
	sortQueue(items, "SortName", false)
	for _, item := range items {
		if _, ok := item["_QueuePath"]; ok {
			t.Fatal("private sorting field exposed")
		}
	}
}

func TestPhotoCardsRecognizeAllJellyfinSizingParameters(t *testing.T) {
	s, _ := fixture(t)
	rec := &recordTransport{base: s.httpClient.Transport}
	s.httpClient.Transport = rec
	var id string
	s.db.QueryRow("SELECT id FROM nodes WHERE kind='Photo'").Scan(&id)
	m, _ := s.media(id)
	for _, size := range []string{
		"fillWidth=320&fillHeight=240",
		"FillWidth=320",
		"fillHeight=240",
		"maxHeight=240",
		"height=240",
		"width=320",
		"MaxWidth=320",
		"fillWidth=0",
		"fillWidth=237.5",
	} {
		w := call(s, "GET", "/Items/"+id+"/Images/Primary?tag="+s.imageTag(m)+"&"+size, "", false)
		if w.Code != 200 || w.Header().Get("X-NAS-Image-Profile") != "thumbnail" || !strings.Contains(rec.urls[len(rec.urls)-1], "/thumb.jpg") {
			t.Fatalf("%s: HTTP %d, upstream %v", size, w.Code, rec.urls)
		}
	}
	w := call(s, "GET", "/Items/"+id+"/Images/Primary?tag="+s.imageTag(m), "", false)
	if w.Code != 200 || w.Header().Get("X-NAS-Image-Profile") != "original" || !strings.Contains(rec.urls[len(rec.urls)-1], "/p/NDLNA/4.jpg") {
		t.Fatal("full photo no longer opens", w.Code, rec.urls)
	}
	m.CoverURL = m.SourceURL
	m.SmallCoverURL = m.SourceURL
	b, _ := json.Marshal(m)
	s.db.Exec("UPDATE nodes SET body=? WHERE id=?", string(b), id)
	before := len(rec.urls)
	w = call(s, "GET", "/Items/"+id+"/Images/Primary?fillWidth=320&tag="+s.imageTag(m), "", false)
	if w.Code != 404 || len(rec.urls) != before {
		t.Fatal("thumbnail request fetched an original", w.Code, rec.urls)
	}
}

func TestVideoCardsKeepPreparedCoverAndSyncPlayHidden(t *testing.T) {
	s, _ := fixture(t)
	id := stableID("/volume1/video/Фильм.mkv")
	m, _ := s.media(id)
	m.SmallCoverURL = s.cfg.SynologyMediaBase + "/tiny-video.jpg"
	b, _ := json.Marshal(m)
	s.db.Exec("UPDATE nodes SET body=? WHERE id=?", string(b), id)
	rec := &recordTransport{base: s.httpClient.Transport}
	s.httpClient.Transport = rec
	w := call(s, "GET", "/Items/"+id+"/Images/Primary?fillWidth=320&tag="+s.imageTag(m), "", false)
	if w.Code != 200 || !strings.Contains(rec.urls[len(rec.urls)-1], "/v/NDLNA/1.mkv?albumArt=true") {
		t.Fatal(w.Code, rec.urls)
	}
	if s.user()["Policy"].(M)["SyncPlayAccess"] != "None" {
		t.Fatal("unsupported sync play enabled")
	}
}

func TestPhotoDiagnosticsAreProtectedAndNeverContainPhotoContents(t *testing.T) {
	s, _ := fixture(t)
	var id string
	s.db.QueryRow("SELECT id FROM nodes WHERE kind='Photo'").Scan(&id)
	m, _ := s.media(id)
	call(s, "GET", "/Items/"+id+"/Images/Primary?tag="+s.imageTag(m), "", false)
	if w := call(s, "GET", "/NAS/ImageDiagnostics", "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	v := jsonBody(t, call(s, "GET", "/NAS/ImageDiagnostics", "", true))
	events := v["Events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["Profile"] != "original" {
		t.Fatal(v)
	}
}
