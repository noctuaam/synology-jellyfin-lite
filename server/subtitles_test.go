package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubtitleDelivery(t *testing.T) {
	s, _ := fixture(t)
	m := Media{
		ID:   "subtitle-test",
		Kind: "Video",
		Size: 100,
		Streams: []M{
			{
				"Index": 0,
				"Type":  "Video",
				"Codec": "h264",
			},
			{
				"Index":    13,
				"Type":     "Subtitle",
				"Codec":    "subrip",
				"Language": "rus",
			},
			{
				"Index": 14,
				"Type":  "Subtitle",
				"Codec": "hdmv_pgs_subtitle",
			},
		},
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO nodes VALUES(?,?,?,?,?,?,?,?,?)", m.ID, "", "video", m.Kind, "test", "test", "test", "test", string(body)); err != nil {
		t.Fatal(err)
	}
	streams := s.playbackStreams(m)
	track := streams[1]
	if track["DeliveryMethod"] != "External" || track["Codec"] != "webvtt" || track["Index"] != 13 {
		t.Fatal(track)
	}
	if m.Streams[1]["Codec"] != "subrip" || streams[2]["DeliveryMethod"] == "External" {
		t.Fatal("changed catalog metadata or advertised bitmap subtitles as text")
	}
	cache := filepath.Join(s.cfg.Data, "subtitles", s.subtitleCacheKey(m, 13)+".vtt")
	if err = os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nTest\n"
	if err = os.WriteFile(cache, []byte(vtt), 0600); err != nil {
		t.Fatal(err)
	}
	path := track["DeliveryUrl"].(string)
	w := call(s, "GET", path, "", false)
	if w.Code != 200 || w.Body.String() != vtt || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/vtt") {
		t.Fatal(w.Code, w.Body.String())
	}
	jsonPath := strings.Replace(path, ".vtt", ".js", 1)
	responseJSON := jsonBody(t, call(s, "GET", jsonPath, "", false))
	events := responseJSON["TrackEvents"].([]any)
	if len(events) != 1 {
		t.Fatal("missing JSON cues", responseJSON)
	}
	event := events[0].(map[string]any)
	if event["StartPositionTicks"] != float64(10000000) || event["EndPositionTicks"] != float64(30000000) || event["Text"] != "Test" {
		t.Fatal("wrong JSON cue timing or text", event)
	}
	for _, invalid := range []string{
		strings.Replace(path, "tag=", "tag=bad", 1),
		strings.Replace(path, "/13/", "/14/", 1),
		strings.Split(path, "?")[0],
		strings.Split(jsonPath, "?")[0],
	} {
		if w = call(s, "GET", invalid, "", false); w.Code != 401 {
			t.Fatalf("unsigned or altered subtitle accepted: %d", w.Code)
		}
	}
	if w = call(s, "GET", strings.Replace(path, "/13/", "/14/", 1), "", true); w.Code != 404 {
		t.Fatal("bitmap track treated as WebVTT", w.Code)
	}
	response := jsonBody(t, call(s, "POST", "/Items/"+m.ID+"/PlaybackInfo", `{"SubtitleStreamIndex":13}`, true))
	source := response["MediaSources"].([]any)[0].(map[string]any)
	if source["DefaultSubtitleStreamIndex"] != float64(13) {
		t.Fatal("requested subtitle selection was discarded", source)
	}
}

func TestWebVTTJSONEvents(t *testing.T) {
	text := "\ufeffWEBVTT\r\n\r\nNOTE comment\r\nignore\r\n\r\ncue-1\r\n01:02:03.456 --> 01:02:05.001 align:start\r\n<i>Привет</i>\r\nSecond line\r\n\r\n02:03.100 --> 02:05.500\r\nNext\r\n"
	events, err := parseSubtitleEvents(text)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	if events[0].StartPositionTicks != 37234560000 || events[0].EndPositionTicks != 37250010000 || events[0].Text != "<i>Привет</i>\nSecond line" || events[1].StartPositionTicks != 1231000000 {
		t.Fatal(events)
	}
	for _, text := range []string{"not VTT", "WEBVTT\n\nbad --> 00:01.000\nText", "WEBVTT\n\n00:02.000 --> 00:01.000\nText"} {
		if _, err := parseSubtitleEvents(text); err == nil {
			t.Fatal("accepted malformed subtitles", text)
		}
	}
}

func TestSubtitleWebClientFixture(t *testing.T) {
	path := os.Getenv("NAS_TEST_VTT")
	if path == "" {
		t.Skip("set NAS_TEST_VTT to validate an existing extracted subtitle")
	}
	events, err := readSubtitleEvents(path)
	if err != nil || len(events) == 0 {
		t.Fatal("no usable cues", err)
	}
	data, err := json.Marshal(M{"TrackEvents": events})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), "trackevents.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Validated %d cues for the web player", len(events))
}

func TestSubtitleExtractionIntegration(t *testing.T) {
	binary := os.Getenv("NAS_TEST_FFMPEG")
	if binary == "" {
		t.Skip("set NAS_TEST_FFMPEG for a real MKV-to-WebVTT extraction test")
	}
	dir := t.TempDir()
	srt := filepath.Join(dir, "test.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nSubtitle test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mkv := filepath.Join(dir, "test.mkv")
	command := exec.Command(binary, "-v", "error", "-f", "lavfi", "-i", "color=size=32x32:duration=1", "-i", srt, "-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-c:s", "srt", mkv)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("albumArt") != "false" {
			t.Error("missing explicit media mode")
		}
		http.ServeFile(w, r, mkv)
	}))
	defer upstream.Close()
	s := &Server{sharedState: &sharedState{cfg: Config{
		FFmpeg:             binary,
		Data:               dir,
		SynologyMediaBase:  upstream.URL,
		SynologyControlURL: upstream.URL,
	}}}
	cache := filepath.Join(dir, "subtitles", "test.vtt")
	if err := s.extractSubtitle(context.Background(), Media{SourceURL: upstream.URL + "/test.mkv"}, 1, cache); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cache)
	if err != nil || !bytes.HasPrefix(data, []byte("WEBVTT")) || !bytes.Contains(data, []byte("Subtitle test")) {
		t.Fatalf("bad converted subtitle: %s, %v", data, err)
	}
	if _, err = os.Stat(mkv); err != nil {
		t.Fatal("input media was changed", err)
	}
}

func TestSubtitleOutputLimit(t *testing.T) {
	var output bytes.Buffer
	w := &subtitleWriter{
		writer:    &output,
		remaining: 4,
	}
	if _, err := w.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("5")); err == nil || output.String() != "1234" {
		t.Fatal("subtitle size limit not enforced")
	}
}

func TestSubtitleLocalInputStaysInsideVideoRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "movie.mkv")
	outside := filepath.Join(t.TempDir(), "outside.mkv")
	for _, name := range []string{inside, outside} {
		if err := os.WriteFile(name, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{sharedState: &sharedState{cfg: Config{Roots: []RootSpec{{
		Key:       "video",
		LocalPath: root,
	}}}}}
	if s.localSubtitleInput(Media{Path: inside}) == "" || s.localSubtitleInput(Media{Path: outside}) != "" {
		t.Fatal("local subtitle input escaped the configured video root")
	}
}
