// NAS Media Folders: a direct-play Jellyfin API subset, not Jellyfin Server.
package main

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// M represents a dynamic JSON object at the Jellyfin protocol boundary.
type M = map[string]any

const userID = "11111111111111111111111111111111"
const libraryID = "22222222222222222222222222222222"
const rootID = "44444444444444444444444444444444"
const serverID = "33333333333333333333333333333333"

// RootSpec describes a physical folder root in the Synology index.
type RootSpec struct {
	Key       string
	Name      string
	ObjectID  string
	LocalPath string
	MediaType string
}

// Config contains listener, library and Synology connection settings.
type Config struct {
	ServerName         string
	ServerID           string
	DLNAUserAgent      string
	DLNAXUserAgent     string
	DefaultPageSize    int
	Listen             string
	PublicURL          string
	Data               string
	Web                string
	FFmpeg             string
	SynologyControlURL string
	SynologyMediaBase  string
	MetadataFile       string
	Roots              []RootSpec
	RefreshSeconds     int
}

// Credentials stores the viewer account and salted password hash.
type Credentials struct {
	Username string
	Salt     string
	Hash     string
}

// Server serves the Jellyfin API subset backed by the Synology catalog.
type Server struct {
	*sharedState
	identity *Account
}

// sharedState owns process-wide locks and caches; request views only add identity.
type sharedState struct {
	metadataStamp    time.Time
	accountsMu       sync.Mutex
	cfg              Config
	db               *sql.DB
	cred             Credentials
	scan             sync.Mutex
	subtitleMu       sync.Mutex
	loginMu          sync.Mutex
	loginWindow      time.Time
	loginCount       int
	httpClient       *http.Client
	statusMu         sync.RWMutex
	lastSync         time.Time
	lastError        string
	sourceUpdateID   string
	webQueueOnce     sync.Once
	webQueue         []byte
	webQueueError    error
	imageDiagnostics []M
}

// Media contains indexed metadata and existing media resource URLs.
type Media struct {
	ID            string
	Name          string
	Path          string
	Container     string
	Cover         string
	ParentID      string
	SourceID      string
	RootKey       string
	Kind          string
	MediaType     string
	SourceURL     string
	CoverURL      string
	SmallCoverURL string
	RelPath       string
	Artist        string
	Album         string
	Modified      string
	Size          int64
	MTime         int64
	Ticks         int64
	Width         int
	Height        int
	Channels      int
	SampleRate    int
	BitRate       int
	Track         int
	Streams       []M
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func passwordHash(pass, salt string) string {
	key, err := pbkdf2.Key(sha256.New, pass, []byte(salt), 100000, 32)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(key)
}

func describeStream(v M) {
	lang, _ := v["Language"].(string)
	label := map[string]string{
		"rus": "Русский",
		"ru":  "Русский",
		"eng": "Английский",
		"en":  "Английский",
		"ukr": "Украинский",
		"deu": "Немецкий",
		"ger": "Немецкий",
		"fra": "Французский",
		"jpn": "Японский",
		"und": "Язык не указан",
	}[strings.ToLower(lang)]
	if label == "" {
		if lang == "" {
			label = "Язык не указан"
		} else {
			label = lang
		}
	}
	v["DisplayLanguage"] = label
	codec, _ := v["Codec"].(string)
	codec = strings.ToUpper(codec)
	title, _ := v["Title"].(string)
	parts := []string{label}
	if title != "" {
		parts = append(parts, title)
	}
	if codec != "" {
		parts = append(parts, codec)
	}
	if v["Type"] == "Audio" {
		channels := 0
		switch n := v["Channels"].(type) {
		case int:
			channels = n
		case float64:
			channels = int(n)
		}
		ch := map[int]string{
			1: "Моно",
			2: "Стерео",
			6: "5.1",
			8: "7.1",
		}[channels]
		if ch == "" && channels > 0 {
			ch = fmt.Sprintf("%d каналов", channels)
		}
		if ch != "" {
			parts = append(parts, ch)
		}
	}
	if v["IsForced"] == true {
		parts = append(parts, "Принудительные")
	}
	if v["IsHearingImpaired"] == true {
		parts = append(parts, "Для слабослышащих")
	}
	v["DisplayTitle"] = strings.Join(parts, " — ")
	if v["Type"] == "Subtitle" {
		v["IsTextSubtitle"] = codec == "SUBRIP" || codec == "ASS" || codec == "SSA" || codec == "WEBVTT" || codec == "SRT"
		v["DeliveryMethod"] = "Embed"
	}
}

func main() {
	config := flag.String("config", "config.json", "config file")
	printData := flag.Bool("print-data-directory", false, "print the configured state directory without opening it")
	prepareConfig := flag.String("prepare-package-config", "", "write a standalone DSM configuration without changing application data")
	initUsername := flag.String("username", "admin", "administrator name for a fresh initialization")
	initData := flag.Bool("init", false, "initialize one viewer account; prints generated password once")
	verify := flag.Bool("verify", false, "verify imported roots, access protection, covers and range streams")
	scan := flag.Bool("scan", false, "import existing Synology DLNA index and exit")
	flag.Parse()
	b, err := os.ReadFile(*config)
	if err != nil {
		log.Fatal(err)
	}
	var cfg Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		log.Fatal(err)
	}
	configDirectory, err := filepath.Abs(filepath.Dir(*config))
	if err != nil {
		log.Fatal(err)
	}
	if cfg.Data == "" {
		cfg.Data = filepath.Join(configDirectory, "data")
	}
	for _, path := range []*string{&cfg.Data, &cfg.Web, &cfg.FFmpeg, &cfg.MetadataFile} {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = filepath.Join(configDirectory, *path)
		}
	}
	configureDefaults(&cfg)
	if err = loadConfigurationChanges(&cfg); err != nil {
		log.Fatal(err)
	}
	if *printData {
		if strings.ContainsAny(cfg.Data, "\r\n") {
			log.Fatal("invalid data path")
		}
		fmt.Println(cfg.Data)
		return
	}
	if *prepareConfig != "" {
		if err = preparePackageConfiguration(cfg, *prepareConfig); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Configuration prepared; data and credentials unchanged.")
		return
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8097"
	}
	if err = os.MkdirAll(cfg.Data, 0700); err != nil {
		log.Fatal(err)
	}
	cp := filepath.Join(cfg.Data, "credentials.json")
	if *initData {
		salt := randomHex(16)
		pass := randomHex(12)
		c := Credentials{*initUsername, salt, passwordHash(pass, salt)}
		b, _ = json.MarshalIndent(c, "", "  ")
		f, e := os.OpenFile(cp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			log.Fatal(e)
		}
		_, e = f.Write(b)
		f.Close()
		if e != nil {
			log.Fatal(e)
		}
		fmt.Printf("Username: %s\nPassword: %s\n", c.Username, pass)
		return
	}
	b, err = os.ReadFile(cp)
	if err != nil {
		log.Fatal("initialize account first: ", err)
	}
	var c Credentials
	if err = json.Unmarshal(b, &c); err != nil {
		log.Fatal(err)
	}
	s, err := openServer(cfg, c)
	if err != nil {
		log.Fatal(err)
	}
	defer s.db.Close()
	debug.SetMemoryLimit(80 << 20)
	debug.SetGCPercent(60)
	if *verify {
		if err = s.verifyCatalog(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *scan {
		if err = s.syncIndex(context.Background(), true); err != nil {
			log.Fatal(err)
		}
		return
	}
	debug.SetMemoryLimit(80 << 20)
	debug.SetGCPercent(60)
	sCtx, sCancel := context.WithCancel(context.Background())
	defer sCancel()
	go s.refreshLoop(sCtx)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       45 * time.Second,
		MaxHeaderBytes:    32768,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	log.Printf("NAS Media Folders 2.0.0 listening on %s; Synology index source; no media probing or video transcoding", cfg.Listen)
	if err = srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func stableID(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:16])
}

func (s *Server) user() M {
	u := M{
		"Id":                        s.profileID(),
		"Name":                      s.accountName(),
		"NASRole":                   s.accountRole(),
		"ServerId":                  s.serverIdentity(),
		"HasPassword":               true,
		"HasConfiguredPassword":     true,
		"HasConfiguredEasyPassword": false,
		"EnableAutoLogin":           false,
		"Configuration": M{
			"AudioLanguagePreference":    "rus",
			"SubtitleLanguagePreference": "rus",
			"PlayDefaultAudioTrack":      true,
			"SubtitleMode":               "Default",
			"DisplayMissingEpisodes":     false,
			"GroupedFolders":             []string{},
			"MyMediaExcludes":            []string{},
			"OrderedViews":               []string{},
			"LatestItemsExcludes":        []string{},
			"HidePlayedInLatest":         false,
			"RememberAudioSelections":    true,
			"RememberSubtitleSelections": true,
			"EnableNextEpisodeAutoPlay":  false,
		},
		"Policy": M{
			"IsAdministrator":                false,
			"IsDisabled":                     false,
			"IsHidden":                       false,
			"EnableMediaPlayback":            true,
			"EnableAudioPlaybackTranscoding": false,
			"EnableVideoPlaybackTranscoding": false,
			"EnablePlaybackRemuxing":         false,
			"EnableContentDownloading":       false,
			"EnableContentDeletion":          false,
			"EnableAllFolders":               true,
			"EnableAllDevices":               true,
			"EnableRemoteAccess":             false,
			"SyncPlayAccess":                 "None",
			"EnableSyncTranscoding":          false,
			"EnableLiveTvAccess":             false,
			"EnableLiveTvManagement":         false,
			"EnableCollectionManagement":     false,
			"BlockedTags":                    []string{},
			"BlockedMediaFolders":            []string{},
			"EnabledFolders":                 []string{},
			"EnabledDevices":                 []string{},
			"AuthenticationProviderId":       "NASJellyLite",
			"PasswordResetProviderId":        "NASJellyLite",
		},
	}
	u["Configuration"] = s.playbackConfiguration()
	return u
}

func (s *Server) system() M {
	return M{
		"Id":                     s.serverIdentity(),
		"ServerName":             s.cfg.ServerName,
		"Version":                "10.10.7",
		"ProductName":            "Jellyfin Server",
		"OperatingSystem":        "Linux",
		"StartupWizardCompleted": true,
		"LocalAddress":           s.cfg.PublicURL,
		"SupportsLibraryMonitor": false,
		"WebSocketPortNumber":    s.listenerPort(),
		"CanSelfRestart":         false,
		"CanLaunchWebBrowser":    false,
		"HasPendingRestart":      false,
		"IsShuttingDown":         false,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func token(r *http.Request) string {
	if t := r.Header.Get("X-MediaBrowser-Token"); t != "" {
		return t
	}
	if t := r.Header.Get("X-Emby-Token"); t != "" {
		return t
	}
	if t := r.URL.Query().Get("api_key"); t != "" {
		return t
	}
	for _, h := range []string{"Authorization", "X-Emby-Authorization"} {
		v := r.Header.Get(h)
		if strings.HasPrefix(v, "Bearer ") {
			return strings.TrimPrefix(v, "Bearer ")
		}
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "MediaBrowser "))
			if strings.HasPrefix(part, "Token=") {
				return strings.Trim(strings.TrimPrefix(part, "Token="), "\"")
			}
		}
	}
	return ""
}

func (s *Server) authenticated(r *http.Request) bool {
	_, err := s.accountForToken(r)
	return err == nil
}

func query(r *http.Request, name string) string {
	for k, v := range r.URL.Query() {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A request must derive identity from its own token or signed resource URL.
	// Internal callers may hold an account view; never inherit that privilege.
	s = &Server{sharedState: s.sharedState}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Only reflect app/browser origins used by a Jellyfin client, never credentials.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-Emby-Authorization, X-Emby-Token, X-MediaBrowser-Token, Content-Type, Range")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, X-NAS-Image-Profile")
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return
	}
	p := strings.Trim(r.URL.Path, "/")
	if strings.HasPrefix(strings.ToLower(p), "emby/") {
		p = p[5:]
	}
	if account, err := s.accountForToken(r); err == nil {
		s = &Server{
			sharedState: s.sharedState,
			identity:    &account,
		}
	}
	lo := strings.ToLower(p)
	// Jellyfin Web mixes legacy ApiClient routes with newer generated SDK routes.
	switch lo {
	case "userviews":
		p = "Users/" + s.profileID() + "/Views"
	case "userviews/groupingoptions":
		p = "Users/" + s.profileID() + "/GroupingOptions"
	case "items/latest":
		p = "Users/" + s.profileID() + "/Items/Latest"
	case "useritems/resume":
		p = "Items/Resume"
	}
	if strings.HasPrefix(lo, "userfavoriteitems/") {
		p = "Users/" + s.profileID() + "/FavoriteItems/" + strings.Split(p, "/")[1]
	}
	if strings.HasPrefix(lo, "userplayeditems/") {
		p = "Users/" + s.profileID() + "/PlayedItems/" + strings.Split(p, "/")[1]
	}
	lo = strings.ToLower(p)
	if p == "" {
		http.Redirect(w, r, "/web/index.html", 302)
		return
	}
	if lo == "nas-video-navigation.js" {
		s.serveVideoNavigation(w, r)
		return
	}
	if lo == "nas-legacy-photo.js" {
		s.serveLegacyPhotoScript(w, r)
		return
	}
	if lo == "manage" || lo == "manage/" {
		s.serveManagementPage(w, r)
		return
	}
	if lo == "web" || strings.HasPrefix(lo, "web/") {
		if r.URL.Path == "/web" {
			http.Redirect(w, r, "/web/", http.StatusMovedPermanently)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if lo == "web/photoplayer-plugin.d901b9200b6b6a1e1abf.chunk.js" || lo == "web/runtime.bundle.js" || lo == "web/27884.448518055bdb8b476d31.chunk.js" {
			s.serveQueueAsset(w, r, filepath.Base(r.URL.Path))
			return
		}
		if lo == "web/main.jellyfin.bundle.js" {
			s.serveQueueClient(w, r)
			return
		}
		if r.URL.Path == "/web/" || r.URL.Path == "/web/index.html" {
			body, e := os.ReadFile(filepath.Join(s.cfg.Web, "index.html"))
			if e != nil {
				http.Error(w, "web client unavailable", 503)
				return
			}
			// The client increments StartIndex by its own page size. Set that
			// size before its bundles run; silently truncating API pages skips files.
			pageSize := strconv.Itoa(s.libraryPageSize())
			boot := `<script>/* NAS_LOW_MEMORY_PROFILE */try{`
			rows, err := s.db.Query("SELECT id FROM accounts WHERE disabled=0")
			if err == nil {
				for rows.Next() {
					var id string
					if rows.Scan(&id) == nil {
						key, _ := json.Marshal(id + "-libraryPageSize")
						boot += `localStorage.setItem(` + string(key) + `,"` + pageSize + `");`
					}
				}
				rows.Close()
			}
			boot += `}catch(e){}</script>`
			body = bytes.Replace(body, []byte("</head>"), []byte(boot+`<script src="/nas-legacy-photo.js?v=200"></script><script src="/nas-video-navigation.js?v=201"></script></head>`), 1)
			body = bytes.Replace(body, []byte("main.jellyfin.bundle.js?"), []byte("main.jellyfin.bundle.js?nasQueue=200&"), 1)
			body = bytes.Replace(body, []byte("runtime.bundle.js?"), []byte("runtime.bundle.js?nasQueue=200&"), 1)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(body))
			return
		}
		h := http.StripPrefix("/web/", http.FileServer(http.Dir(s.cfg.Web)))
		h.ServeHTTP(w, r)
		return
	}
	if lo == "health" {
		writeJSON(w, 200, M{
			"Status":         "Healthy",
			"Implementation": "NAS Media Folders",
			"Version":        "2.0.0",
		})
		return
	}
	if lo == "system/info/public" || lo == "system/ping" {
		writeJSON(w, 200, s.system())
		return
	}
	if lo == "branding/configuration" {
		writeJSON(w, 200, M{
			"LoginDisclaimer":     "Лёгкий сервер папок NAS. Прямое воспроизведение без перекодирования.",
			"CustomCss":           "",
			"SplashscreenEnabled": false,
		})
		return
	}
	if lo == "branding/css" {
		w.Header().Set("Content-Type", "text/css")
		return
	}
	if lo == "users/public" {
		writeJSON(w, 200, s.publicAccounts())
		return
	}
	if lo == "quickconnect/enabled" {
		writeJSON(w, 200, false)
		return
	}
	if lo == "users/authenticatebyname" && r.Method == "POST" {
		s.loginMu.Lock()
		if time.Since(s.loginWindow) > time.Minute {
			s.loginWindow = time.Now()
			s.loginCount = 0
		}
		s.loginCount++
		allowed := s.loginCount <= 12
		s.loginMu.Unlock()
		if !allowed {
			w.WriteHeader(429)
			return
		}
		var body struct{ Username, Pw string }
		if decode(r, &body) != nil {
			w.WriteHeader(400)
			return
		}
		account, valid := s.authenticateAccount(body.Username, body.Pw)
		if !valid {
			w.WriteHeader(401)
			return
		}
		s = &Server{
			sharedState: s.sharedState,
			identity:    &account,
		}

		t := randomHex(32)
		_, e := s.db.Exec("INSERT INTO account_tokens VALUES(?,?,?)", t, time.Now().Add(30*24*time.Hour).Unix(), s.profileID())
		if e != nil {
			w.WriteHeader(500)
			return
		}
		writeJSON(w, 200, M{
			"User":        s.user(),
			"AccessToken": t,
			"ServerId":    s.serverIdentity(),
			"SessionInfo": M{
				"Id":       stableID(t),
				"UserId":   s.profileID(),
				"UserName": s.accountName(),
				"ServerId": s.serverIdentity(),
			},
		})
		return
	}
	if strings.HasPrefix(lo, "items/") && strings.Contains(lo, "/images/") && (r.Method == "GET" || r.Method == "HEAD") {
		parts := strings.Split(p, "/")
		if len(parts) >= 4 {
			if view, valid := s.signedImageView(parts[1], query(r, "Tag")); valid {
				view.image(w, r, parts[1])
				return
			}
		}
	}
	if s.serveSignedSubtitle(w, r, p) {
		return
	}
	if s.identity == nil {
		w.WriteHeader(401)
		return
	}
	if uid := query(r, "UserId"); uid != "" && uid != s.profileID() {
		w.WriteHeader(404)
		return
	}
	mutation := lo == "nas/configuration" || lo == "nas/users" || strings.HasPrefix(lo, "nas/users/") || lo == "nas/settings" || lo == "sessions/logout" || lo == "sessions/capabilities/full" || lo == "sessions/capabilities" || strings.HasPrefix(lo, "sessions/playing") || strings.HasPrefix(lo, "displaypreferences/") || lo == "users/"+s.profileID()+"/configuration" || strings.HasSuffix(lo, "/playbackinfo")
	mark := strings.HasPrefix(lo, "users/"+s.profileID()+"/favoriteitems/") || strings.HasPrefix(lo, "users/"+s.profileID()+"/playeditems/")
	if r.Method != "GET" && r.Method != "HEAD" && !(r.Method == "POST" && (mutation || mark)) && !(r.Method == "DELETE" && mark) {
		w.WriteHeader(405)
		return
	}
	if lo == "sessions/logout" && r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if strings.HasPrefix(lo, "users/") {
		parts := strings.Split(lo, "/")
		if len(parts) > 1 && parts[1] != s.profileID() && parts[1] != "me" {
			w.WriteHeader(404)
			return
		}
	}
	switch {
	case lo == "nas/configuration":
		s.manageConfiguration(w, r)
	case lo == "nas/me":
		writeJSON(w, 200, s.identity)
	case lo == "nas/users" || strings.HasPrefix(lo, "nas/users/"):
		id := strings.TrimPrefix(lo, "nas/users")
		s.manageAccounts(w, r, strings.TrimPrefix(id, "/"))
	case lo == "nas/settings":
		s.managementSettings(w, r)
	case lo == "nas/imagediagnostics":
		if !s.administrator() {
			w.WriteHeader(403)
			return
		}
		s.statusMu.RLock()
		events := append([]M{}, s.imageDiagnostics...)
		s.statusMu.RUnlock()
		writeJSON(w, 200, M{"Events": events})
	case lo == "users/"+s.profileID()+"/items/root":
		f := s.aggregateFolder()
		writeJSON(w, 200, f)
	case lo == "system/info":
		writeJSON(w, 200, s.system())
	case lo == "users/me" || lo == "users/"+s.profileID():
		writeJSON(w, 200, s.user())
	case lo == "users/"+s.profileID()+"/views":
		s.views(w, r)
	case lo == "users/"+s.profileID()+"/groupingoptions":
		writeJSON(w, 200, []M{})
	case lo == "sessions/logout":
		s.db.Exec("DELETE FROM tokens WHERE token=?", token(r))
		s.db.Exec("DELETE FROM account_tokens WHERE token=?", token(r))
		w.WriteHeader(204)
	case lo == "sessions/capabilities/full" || lo == "sessions/capabilities":
		w.WriteHeader(204)
	case lo == "sessions/playing" || lo == "sessions/playing/progress" || lo == "sessions/playing/stopped":
		s.saveProgress(w, r)
	case lo == "system/index/status":
		writeJSON(w, 200, s.indexStatus())
	case lo == "socket":
		s.socket(w, r)
	case lo == "playback/bitratetest":
		n, _ := strconv.Atoi(query(r, "Size"))
		if n < 1 || n > 10000000 {
			n = 1000000
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(n))
		if r.Method != "HEAD" {
			block := make([]byte, 32768)
			for n > 0 {
				k := min(n, len(block))
				if _, e := w.Write(block[:k]); e != nil {
					return
				}
				n -= k
			}
		}
	case lo == "sessions":
		writeJSON(w, 200, []M{})
	case strings.HasPrefix(lo, "displaypreferences/"):
		s.preferences(w, r, p)
	case lo == "users/"+s.profileID()+"/configuration" && r.Method == "POST":
		var configuration M
		if decode(r, &configuration) != nil {
			w.WriteHeader(400)
			return
		}
		if err := s.savePlaybackConfiguration(configuration); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(204)
	case lo == "items/filters" || lo == "items/filters2":
		writeJSON(w, 200, M{
			"Genres": []string{},
			"Tags":   []string{},
			"Years":  []int{},
		})
	case lo == "persons" || lo == "artists" || lo == "artists/albumartists":
		// Global search queries these sections before reaching videos, tracks
		// and photos. Our physical-folder catalog has no person/artist nodes;
		// an empty collection lets the client continue instead of aborting.
		writeJSON(w, 200, M{
			"Items":            []M{},
			"TotalRecordCount": 0,
			"StartIndex":       0,
		})
	case lo == "system/endpoint":
		writeJSON(w, 200, M{
			"IsLocal":     true,
			"IsInNetwork": true,
		})
	case lo == "plugins" || lo == "channels" || lo == "livetv/programs/recommended" || lo == "shows/nextup" || lo == "items/suggestions" || lo == "recommendations":
		writeJSON(w, 200, M{
			"Items":            []M{},
			"TotalRecordCount": 0,
			"StartIndex":       0,
		})
	case lo == "localization/cultures" || lo == "localization/countries" || lo == "localization/parentalratings" || lo == "localization/options":
		writeJSON(w, 200, []M{})
	case strings.HasPrefix(lo, "users/"+s.profileID()+"/favoriteitems/"):
		s.mark(w, r, strings.Split(p, "/")[3], "favorite")
	case strings.HasPrefix(lo, "users/"+s.profileID()+"/playeditems/"):
		s.mark(w, r, strings.Split(p, "/")[3], "played")
	case strings.HasPrefix(lo, "videos/") && strings.Contains(lo, "/subtitles/"):
		s.subtitle(w, r, p)
	case (strings.HasPrefix(lo, "videos/") || strings.HasPrefix(lo, "audio/")) && (strings.Contains(lo, "/stream") || strings.HasSuffix(lo, "/universal")):
		s.stream(w, r, strings.Split(p, "/")[1])
	case strings.HasPrefix(lo, "items/") && strings.HasSuffix(lo, "/instantmix"):
		m, e := s.media(strings.Split(p, "/")[1])
		if e != nil || m.Kind != "Audio" {
			w.WriteHeader(404)
			return
		}
		q := r.URL.Query()
		q.Set("ParentId", m.ParentID)
		q.Set("MediaTypes", "Audio")
		q.Set("Recursive", "false")
		q.Set("SortBy", "Random")
		q.Set("NasPlaybackQueue", "true")
		q.Set("NasQueueSeed", m.ID)
		r.URL.RawQuery = q.Encode()
		s.list(w, r, "items")
	case strings.HasPrefix(lo, "items/") && strings.HasSuffix(lo, "/thememedia"):
		writeJSON(w, 200, M{
			"ThemeSongsResult": M{
				"Items":            []M{},
				"TotalRecordCount": 0,
			},
			"ThemeVideosResult": M{
				"Items":            []M{},
				"TotalRecordCount": 0,
			},
		})
	case strings.HasPrefix(lo, "items/") && strings.HasSuffix(lo, "/playbackinfo"):
		id := strings.Split(p, "/")[1]
		m, e := s.media(id)
		if e != nil {
			w.WriteHeader(404)
			return
		}
		s.playbackInfo(w, r, m)
	case strings.HasPrefix(lo, "items/") && strings.HasSuffix(lo, "/download"):
		s.stream(w, r, strings.Split(p, "/")[1])
	case strings.HasPrefix(lo, "items/") && strings.Contains(lo, "/images/"):
		s.image(w, r, strings.Split(p, "/")[1])
	case lo == "items" || lo == "items/resume" || lo == "users/"+s.profileID()+"/items" || lo == "users/"+s.profileID()+"/items/latest":
		s.list(w, r, lo)
	case strings.HasPrefix(lo, "users/"+s.profileID()+"/items/") || strings.HasPrefix(lo, "items/"):
		parts := strings.Split(p, "/")
		id := parts[len(parts)-1]
		if id == rootID {
			writeJSON(w, 200, s.aggregateFolder())
			return
		}
		if id == libraryID {
			if !s.permitsRoot("video") {
				w.WriteHeader(404)
				return
			}
			writeJSON(w, 200, s.folder())
			return
		}
		if len(parts) > 2 && parts[len(parts)-1] == "Similar" {
			writeJSON(w, 200, M{
				"Items":            []M{},
				"TotalRecordCount": 0,
			})
			return
		}
		m, e := s.media(id)
		if e != nil {
			log.Printf("unsupported GET path: %s", r.URL.EscapedPath())
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, s.item(m))
	default:
		log.Printf("unsupported API: %s %s", r.Method, r.URL.EscapedPath())
		writeJSON(w, 404, M{"Message": "Endpoint not implemented by NAS Media Folders"})
	}
}

func (s *Server) saveProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var b struct {
		ItemID              string `json:"ItemId"`
		PositionTicks       int64
		AudioStreamIndex    *int
		SubtitleStreamIndex *int
	}
	if decode(r, &b) != nil || b.PositionTicks < 0 {
		w.WriteHeader(400)
		return
	}
	m, e := s.media(b.ItemID)
	if e != nil {
		w.WriteHeader(404)
		return
	}
	if err := s.rememberStreams(m, b.AudioStreamIndex, b.SubtitleStreamIndex); err != nil {
		http.Error(w, "Cannot save track selection", 500)
		return
	}
	played := 0
	if m.Ticks > 0 && b.PositionTicks >= m.Ticks*9/10 {
		played = 1
	}
	_, e = s.db.Exec("INSERT INTO progress(id,position,played,lastplayed) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET position=excluded.position,played=max(progress.played,excluded.played),lastplayed=excluded.lastplayed", s.storageKey(b.ItemID), b.PositionTicks, played, time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		w.WriteHeader(500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) mark(w http.ResponseWriter, r *http.Request, id, col string) {
	if r.Method != "POST" && r.Method != "DELETE" {
		w.WriteHeader(405)
		return
	}
	m, e := s.media(id)
	if e != nil {
		w.WriteHeader(404)
		return
	}
	val := 1
	if r.Method == "DELETE" {
		val = 0
	}
	_, e = s.db.Exec("INSERT INTO progress(id,"+col+") VALUES(?,?) ON CONFLICT(id) DO UPDATE SET "+col+"=excluded."+col, s.storageKey(id), val)
	if e != nil {
		w.WriteHeader(500)
		return
	}
	writeJSON(w, 200, s.item(m)["UserData"])
}

func (s *Server) preferences(w http.ResponseWriter, r *http.Request, p string) {
	id := strings.TrimPrefix(strings.ToLower(p), "displaypreferences/")
	v := M{
		"Id":               id,
		"ViewType":         "Poster",
		"SortBy":           "SortName",
		"SortOrder":        "Ascending",
		"RememberIndexing": false,
		"RememberSorting":  false,
		"ScrollDirection":  "Horizontal",
		"ShowBackdrop":     true,
		"ShowSidebar":      false,
		"CustomPrefs":      M{},
	}
	if r.Method == "POST" {
		if decode(r, &v) != nil {
			w.WriteHeader(400)
			return
		}
	} else {
		var raw string
		if s.db.QueryRow("SELECT body FROM prefs WHERE id=?", s.storageKey(id)).Scan(&raw) == nil {
			json.Unmarshal([]byte(raw), &v)
		}
	}
	if id == "usersettings" {
		prefs, ok := v["CustomPrefs"].(map[string]any)
		if !ok {
			prefs = M{}
		}
		prefs["homesection0"] = "smalllibrarytiles"
		for i := 1; i < 10; i++ {
			prefs[fmt.Sprintf("homesection%d", i)] = "none"
		}
		v["CustomPrefs"] = prefs
	}
	if r.Method == "POST" {
		b, _ := json.Marshal(v)
		if _, e := s.db.Exec("INSERT INTO prefs VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", s.storageKey(id), string(b)); e != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 200, v)
}
