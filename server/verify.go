package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"time"
)

// Run as the service account during installation, using the actual catalog and
// upstream resources. The temporary token is removed, and no user state changes.
func (s *Server) verifyCatalog() error {
	var administratorID string
	if err := s.db.QueryRow("SELECT id FROM accounts WHERE role='admin' AND disabled=0 ORDER BY id LIMIT 1").Scan(&administratorID); err != nil {
		return err
	}
	view, valid := s.accountView(administratorID)
	if !valid {
		return fmt.Errorf("no enabled administrator for verification")
	}
	s = view
	t := randomHex(32)
	if _, e := s.db.Exec("INSERT INTO account_tokens VALUES(?,?,?)", t, time.Now().Add(time.Minute).Unix(), s.profileID()); e != nil {
		return e
	}
	defer s.db.Exec("DELETE FROM account_tokens WHERE token=?", t)
	request := func(address string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", address, nil)
		if auth {
			r.Header.Set("X-Emby-Token", t)
		}
		r.Header.Set("Range", "bytes=0-1023")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := request("/Items", false); w.Code != 401 {
		return fmt.Errorf("anonymous catalog: HTTP %d", w.Code)
	}
	views := request("/UserViews", true)
	var v struct{ Items []M }
	if views.Code != 200 || json.Unmarshal(views.Body.Bytes(), &v) != nil || len(v.Items) != len(s.cfg.Roots) {
		return fmt.Errorf("invalid folder views: HTTP %d", views.Code)
	}
	fmt.Printf("PASS: %d folder roots; anonymous catalog requires authentication\n", len(v.Items))
	for _, root := range s.cfg.Roots {
		var n int
		if e := s.db.QueryRow("SELECT count(*) FROM nodes WHERE root=? AND kind NOT IN ('Folder','CollectionFolder')", root.Key).Scan(&n); e != nil {
			return e
		}
		if n < 1 {
			return fmt.Errorf("empty imported root: %s", root.Key)
		}
		fmt.Printf("%s: %d items\n", root.Key, n)
	}
	for _, kind := range []string{"Video", "Audio", "Photo"} {
		var id string
		if e := s.db.QueryRow("SELECT id FROM nodes WHERE kind=? ORDER BY sortname LIMIT 1", kind).Scan(&id); e != nil {
			return e
		}
		m, e := s.media(id)
		if e != nil {
			return e
		}
		address := "/Items/" + id + "/Download"
		if kind == "Audio" {
			address = "/Audio/" + id + "/universal"
		}
		if w := request(address, true); w.Code != 200 && w.Code != 206 {
			return fmt.Errorf("%s stream: HTTP %d; %s", kind+" "+m.SourceURL, w.Code, w.Body.String())
		}
		if tag := s.imageTag(m); tag != "" {
			w := request("/Items/"+id+"/Images/Primary?MaxWidth=320&tag="+tag, false)
			if w.Code != 200 && w.Code != 206 {
				return fmt.Errorf("%s signed cover: HTTP %d", kind, w.Code)
			}
			fmt.Printf("PASS: %s range stream and existing cover\n", kind)
		} else {
			fmt.Printf("PASS: %s range stream (no source cover)\n", kind)
		}
	}
	return nil
}
