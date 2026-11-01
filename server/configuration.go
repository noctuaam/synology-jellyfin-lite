package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// ConfigurationChanges contains settings editable without moving application
// state. New values apply after a controlled service restart.
type ConfigurationChanges struct {
	ServerName         string
	Listen             string
	PublicURL          string
	SynologyControlURL string
	SynologyMediaBase  string
	DLNAUserAgent      string
	DLNAXUserAgent     string
	FFmpeg             string
	MetadataFile       string
	RefreshSeconds     int
	Roots              []RootSpec
}

func configurationChanges(config Config) ConfigurationChanges {
	return ConfigurationChanges{
		ServerName:         config.ServerName,
		Listen:             config.Listen,
		PublicURL:          config.PublicURL,
		SynologyControlURL: config.SynologyControlURL,
		SynologyMediaBase:  config.SynologyMediaBase,
		DLNAUserAgent:      config.DLNAUserAgent,
		DLNAXUserAgent:     config.DLNAXUserAgent,
		FFmpeg:             config.FFmpeg,
		MetadataFile:       config.MetadataFile,
		RefreshSeconds:     config.RefreshSeconds,
		Roots:              config.Roots,
	}
}

func (changes ConfigurationChanges) apply(config *Config) {
	config.ServerName = changes.ServerName
	config.Listen = changes.Listen
	config.PublicURL = changes.PublicURL
	config.SynologyControlURL = changes.SynologyControlURL
	config.SynologyMediaBase = changes.SynologyMediaBase
	config.DLNAUserAgent = changes.DLNAUserAgent
	config.DLNAXUserAgent = changes.DLNAXUserAgent
	config.FFmpeg = changes.FFmpeg
	config.MetadataFile = changes.MetadataFile
	config.RefreshSeconds = changes.RefreshSeconds
	config.Roots = changes.Roots
}

func validateConfiguration(changes ConfigurationChanges) error {
	if strings.TrimSpace(changes.ServerName) == "" || len(changes.ServerName) > 128 {
		return errors.New("server name is required")
	}
	_, port, err := net.SplitHostPort(changes.Listen)
	if err != nil {
		return errors.New("listen address must contain host and port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("invalid listener port")
	}
	for _, origin := range []string{changes.SynologyControlURL, changes.SynologyMediaBase} {
		if err := validateOrigin(origin); err != nil {
			return err
		}
	}
	if changes.PublicURL != "" {
		origin, err := url.Parse(changes.PublicURL)
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
			return errors.New("public URL must be an HTTP or HTTPS origin")
		}
	}
	for _, header := range []string{changes.DLNAUserAgent, changes.DLNAXUserAgent} {
		if strings.ContainsAny(header, "\r\n") {
			return errors.New("invalid DLNA user agent")
		}
	}
	if changes.RefreshSeconds < 60 || changes.RefreshSeconds > 86400 {
		return errors.New("refresh interval must be 60–86400 seconds")
	}
	if len(changes.Roots) < 1 || len(changes.Roots) > 3 {
		return errors.New("configure one to three media roots")
	}
	seen := map[string]bool{}
	for _, root := range changes.Roots {
		expected := map[string]string{
			"video": "Video",
			"music": "Audio",
			"photo": "Photo",
		}[root.Key]
		if expected == "" || seen[root.Key] || root.MediaType != expected || root.Name == "" || root.ObjectID == "" || !filepath.IsAbs(root.LocalPath) || filepath.Dir(root.LocalPath) == root.LocalPath {
			return fmt.Errorf("invalid media root: %s", root.Key)
		}
		seen[root.Key] = true
	}
	for _, path := range []string{changes.FFmpeg, changes.MetadataFile} {
		if path != "" && !filepath.IsAbs(path) {
			return errors.New("tool and metadata paths must be absolute")
		}
	}
	return nil
}

func loadConfigurationChanges(config *Config) error {
	data, err := os.ReadFile(filepath.Join(config.Data, "server-settings.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var changes ConfigurationChanges
	if err = json.Unmarshal(data, &changes); err != nil {
		return err
	}
	if err = validateConfiguration(changes); err != nil {
		return err
	}
	changes.apply(config)
	return nil
}

func (s *Server) manageConfiguration(w http.ResponseWriter, r *http.Request) {
	if !s.administrator() {
		http.Error(w, "Administrator access required", 403)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	active := configurationChanges(s.cfg)
	if r.Method == "GET" {
		configured := s.cfg
		if err := loadConfigurationChanges(&configured); err != nil {
			http.Error(w, "Saved configuration is unreadable", 500)
			return
		}
		writeJSON(w, 200, M{
			"Configuration":   configurationChanges(configured),
			"RestartRequired": !reflect.DeepEqual(active, configurationChanges(configured)),
			"DataDirectory":   s.cfg.Data,
			"WebDirectory":    s.cfg.Web,
		})
		return
	}
	var changes ConfigurationChanges
	if err := decode(r, &changes); err != nil {
		http.Error(w, "Invalid configuration", 400)
		return
	}
	if err := validateConfiguration(changes); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Never change root keys behind existing profiles. Roots can move or be
	// renamed, but the three stable media identities remain the same.
	data, err := json.MarshalIndent(changes, "", "  ")
	if err != nil {
		http.Error(w, "Cannot encode configuration", 500)
		return
	}
	if err = atomicPrivateFile(filepath.Join(s.cfg.Data, "server-settings.json"), data); err != nil {
		http.Error(w, "Cannot save configuration", 500)
		return
	}
	writeJSON(w, 200, M{"RestartRequired": !reflect.DeepEqual(active, changes)})
}

func atomicPrivateFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func localLANAddress() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, network := range interfaces {
		if network.Flags&net.FlagUp == 0 || network.Flags&net.FlagLoopback != 0 || strings.HasPrefix(network.Name, "docker") || strings.HasPrefix(network.Name, "veth") {
			continue
		}
		addresses, err := network.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}

func (s *Server) initializeServerIdentity() error {
	path := filepath.Join(s.cfg.Data, "server-id")
	if data, err := os.ReadFile(path); err == nil {
		s.cfg.ServerID = strings.TrimSpace(string(data))
	} else if !os.IsNotExist(err) {
		return err
	}
	if s.cfg.ServerID == "" {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM nodes").Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			s.cfg.ServerID = serverID
		} else {
			s.cfg.ServerID = randomHex(16)
		}
	}
	decoded, err := hex.DecodeString(s.cfg.ServerID)
	if err != nil || len(decoded) != 16 {
		return errors.New("invalid server identity")
	}
	if err = atomicPrivateFile(path, []byte(s.cfg.ServerID+"\n")); err != nil {
		return err
	}
	return nil
}

func (s *Server) serverIdentity() string {
	if s.cfg.ServerID != "" {
		return s.cfg.ServerID
	}
	return serverID
}

func (s *Server) listenerPort() int {
	_, raw, err := net.SplitHostPort(s.cfg.Listen)
	if err == nil {
		if port, err := strconv.Atoi(raw); err == nil {
			return port
		}
	}
	return 8097
}
