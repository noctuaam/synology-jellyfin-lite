package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const originalWebHash = "8774a41779f20b4d4ed056434fa0d4630ce7b28e908522e5210a34ab67137672"

// Version-checked patches extend the official queue builder and expose a narrow
// bridge for our folder-navigation controls. Upstream assets remain unchanged.
func patchQueueClient(original []byte) ([]byte, error) {
	hash := sha256.Sum256(original)
	if hex.EncodeToString(hash[:]) != originalWebHash {
		return nil, errors.New("unexpected Jellyfin Web version; queue patch refused")
	}
	s := string(original)
	old := `return function(e,t,n,r){if("Photo"===e.MediaType)`
	new := `return function(e,t,n,r,a){if(("Audio"===e.MediaType||"Video"===e.MediaType)&&(!a||1===a.length))return Ae(t,Pe({ParentId:e.ParentId,Filters:"IsNotFolder",Recursive:!1,SortBy:n.shuffle?"Random":"SortName",MediaTypes:e.MediaType,Limit:-1},r)).then((function(t){var r=t.Items.map((function(e){return e.Id})).indexOf(e.Id);return-1===r&&(r=0),n.startIndex=r,Promise.resolve(t)}));if("Photo"===e.MediaType)`
	if strings.Count(s, old) != 1 {
		return nil, errors.New("queue builder signature mismatch")
	}
	s = strings.Replace(s, old, new, 1)
	old = `return null}(e,t,n,r)}function D()`
	new = `return null}(e,t,n,r,i)}function D()`
	if strings.Count(s, old) != 1 {
		return nil, errors.New("queue context signature mismatch")
	}
	s = strings.Replace(s, old, new, 1)
	old = `t.Fields=["Chapters","Trickplay"],t.ExcludeLocationTypes=`
	new = `t.NasPlaybackQueue="true",t.Fields=["Chapters","Trickplay"],t.ExcludeLocationTypes=`
	if strings.Count(s, old) != 1 {
		return nil, errors.New("playback query signature mismatch")
	}
	s = strings.Replace(s, old, new, 1)
	old = `case"PhotoAlbum":return Ae(t,Pe({ParentId:e.Id,Filters:"IsNotFolder",Recursive:!1,`
	new = `case"PhotoAlbum":return Ae(t,Pe({ParentId:e.Id,Filters:"IsNotFolder",Recursive:!0,`
	if strings.Count(s, old) != 1 {
		return nil, errors.New("photo folder signature mismatch")
	}
	s = strings.Replace(s, old, new, 1)
	old = `var t,r=this,i=[],a=null;this._playNextAfterEnded=!0;`
	new = `var t,r=this,i=[],a=null;window.NASVideoBridge={manager:r,load:function(id){var client=C.A.currentApiClient();return client.getItem(client.getCurrentUserId(),id).then(function(item){if("Video"!==item.MediaType)return{item:item,items:[]};return Ae(item.ServerId,{ParentId:item.ParentId,Recursive:!1,MediaTypes:"Video",SortBy:"SortName",Limit:-1,NasPlaybackQueue:"true"}).then(function(result){return{item:item,items:result.Items}})})}};this._playNextAfterEnded=!0;`
	if strings.Count(s, old) != 1 {
		return nil, errors.New("video navigation bridge signature mismatch")
	}
	s = strings.Replace(s, old, new, 1)
	return []byte("/* NAS_FOLDER_QUEUE_102; modified Jellyfin Web 10.10.7; GPL-2.0 */\n" + s), nil
}

func (s *Server) serveQueueClient(w http.ResponseWriter, r *http.Request) {
	s.webQueueOnce.Do(func() {
		b, e := os.ReadFile(filepath.Join(s.cfg.Web, "main.jellyfin.bundle.js"))
		if e != nil {
			s.webQueueError = e
			return
		}
		s.webQueue, s.webQueueError = patchQueueClient(b)
	})
	if s.webQueueError != nil {
		http.Error(w, "web queue compatibility check failed", 503)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"nas-queue-200"`)
	http.ServeContent(w, r, "main.jellyfin.bundle.js", time.Time{}, bytes.NewReader(s.webQueue))
}

func patchQueueAsset(name string, b []byte) ([]byte, error) {
	var expected, old, replacement string
	switch name {
	case "runtime.bundle.js":
		expected = "027f94a3ba2b80de95cbf5fedf8bcda37496f9e14f4259bccd010c3a538e7616"
		old = `+".chunk.js"`
		replacement = `+".chunk.js?nasQueue=200"`
	case "photoPlayer-plugin.d901b9200b6b6a1e1abf.chunk.js":
		expected = "54193b365acb92f747dba21802a5843525486b9c30de71e7c6ff9c941385cfa9"
		old = `key:"play",value:function(t){return new Promise`
		replacement = `key:"play",value:function(t){var nasChrome=/Chrome\/(\d+)/.exec(navigator.userAgent);if(window.NASLegacyPhotoViewer&&/Web0S|WebOS|SmartTV/i.test(navigator.userAgent)&&nasChrome&&Number(nasChrome[1])<70)return window.NASLegacyPhotoViewer(t);return new Promise`
	case "27884.448518055bdb8b476d31.chunk.js":
		expected = "939f2b55f152b6e21e86734139cdfce116c6e01e9f3b4e0a09f24925fd3406fe"
		old = `virtual:{slides:h,cache:!0,renderSlide:S,addSlidesBefore:1,addSlidesAfter:1}`
		replacement = `virtual:{slides:h,cache:!1,renderSlide:S,addSlidesBefore:1,addSlidesAfter:1}`
	default:
		return nil, errors.New("unknown client asset")
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != expected || bytes.Count(b, []byte(old)) != 1 {
		return nil, errors.New("unexpected client asset version")
	}
	return bytes.Replace(b, []byte(old), []byte(replacement), 1), nil
}

func (s *Server) serveQueueAsset(w http.ResponseWriter, r *http.Request, name string) {
	b, e := os.ReadFile(filepath.Join(s.cfg.Web, name))
	if e == nil {
		b, e = patchQueueAsset(name, b)
	}
	if e != nil {
		http.Error(w, "client asset compatibility check failed", 503)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"nas-queue-200-`+name+`"`)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}

func (s *Server) queueItem(m Media, position int64, played, favorite int) M {
	tags := M{}
	if tag := s.imageTag(m); tag != "" {
		tags["Primary"] = tag
	}
	return M{
		"Id":                m.ID,
		"Name":              m.Name,
		"Type":              m.Kind,
		"MediaType":         m.MediaType,
		"IsFolder":          false,
		"ServerId":          s.serverIdentity(),
		"ParentId":          m.ParentID,
		"Container":         m.Container,
		"RunTimeTicks":      m.Ticks,
		"Size":              m.Size,
		"Width":             m.Width,
		"Height":            m.Height,
		"ImageTags":         tags,
		"BackdropImageTags": []string{},
		"Artists":           []string{m.Artist},
		"Album":             m.Album,
		"IndexNumber":       m.Track,
		"UserData": M{
			"Key":                   m.ID,
			"PlaybackPositionTicks": position,
			"IsFavorite":            favorite != 0,
			"Played":                played != 0,
			"PlayCount":             0,
		},
	}
}

// Compare numeric runs as numbers, so episode 2 precedes episode 10 even when
// filenames have no leading zero. The queue never invents series collections.
func naturalLess(a, b string) bool {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] >= '0' && a[i] <= '9' && b[j] >= '0' && b[j] <= '9' {
			ai, bj := i, j
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			x, y := strings.TrimLeft(a[ai:i], "0"), strings.TrimLeft(b[bj:j], "0")
			if len(x) != len(y) {
				return len(x) < len(y)
			}
			if x != y {
				return x < y
			}
			continue
		}
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func sortQueue(items []M, by string, descending bool) {
	if by == "" || strings.Contains(strings.ToLower(by), "sortname") {
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i]["_QueuePath"].(string), items[j]["_QueuePath"].(string)
			if descending {
				return naturalLess(b, a)
			}
			return naturalLess(a, b)
		})
	}
	for _, item := range items {
		delete(item, "_QueuePath")
	}
}
