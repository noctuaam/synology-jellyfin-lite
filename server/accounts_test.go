package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func accountRequest(s *Server, method, path, body, accessToken string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if accessToken != "" {
		r.Header.Set("X-Emby-Token", accessToken)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func createViewer(t *testing.T, s *Server) (string, string) {
	t.Helper()
	created := jsonBody(t, call(s, "POST", "/NAS/Users", `{"Username":"guest","Password":"guest-password","Role":"viewer","AllowedRoots":["video"]}`, true))
	auth := jsonBody(t, accountRequest(s, "POST", "/Users/AuthenticateByName", `{"Username":"guest","Pw":"guest-password"}`, ""))
	return created["Id"].(string), auth["AccessToken"].(string)
}

func TestAccountsIsolateHistoryAndEnforceLibraries(t *testing.T) {
	s, _ := fixture(t)
	id, accessToken := createViewer(t, s)
	video := stableID("/volume1/video/Фильм.mkv")
	if w := call(s, "POST", "/Users/"+userID+"/FavoriteItems/"+video, "", true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	getItem := func(token string) M {
		return jsonBody(t, accountRequest(s, "GET", "/Items/"+video, "", token))
	}
	if getItem(accessToken)["UserData"].(map[string]any)["IsFavorite"] != false {
		t.Fatal("another user's favorite leaked")
	}
	progress, _ := json.Marshal(M{
		"ItemId":        video,
		"PositionTicks": 30000000,
	})
	if w := accountRequest(s, "POST", "/Sessions/Playing/Progress", string(progress), accessToken); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if getItem(accessToken)["UserData"].(map[string]any)["PlaybackPositionTicks"] != float64(30000000) {
		t.Fatal("viewer progress was not saved")
	}
	if getItem("test")["UserData"].(map[string]any)["PlaybackPositionTicks"] != float64(0) {
		t.Fatal("viewer changed owner's progress")
	}
	for _, path := range []string{"/Items/" + stableID("/volume1/music/Artist / Track.mp3"), "/Users/" + userID + "/Items", "/Items/" + rootKeyID("photo")} {
		if w := accountRequest(s, "GET", path, "", accessToken); w.Code != 404 {
			t.Fatal("access denied library or account", path, w.Code)
		}
	}
	views := jsonBody(t, accountRequest(s, "GET", "/UserViews", "", accessToken))["Items"].([]any)
	if len(views) != 1 || views[0].(map[string]any)["Id"] != libraryID {
		t.Fatal("library permissions ignored", views)
	}
	for _, path := range []string{"/NAS/Users", "/NAS/Settings", "/NAS/ImageDiagnostics"} {
		if w := accountRequest(s, "GET", path, "", accessToken); w.Code != 403 {
			t.Fatal("viewer got administrator access", path, w.Code)
		}
	}
	if w := accountRequest(s, "POST", "/Users/"+id+"/Configuration", `{"EnableNextEpisodeAutoPlay":false}`, accessToken); w.Code != 204 {
		t.Fatal(w.Code)
	}
	owner := jsonBody(t, call(s, "GET", "/Users/Me", "", true))
	if owner["Configuration"].(map[string]any)["EnableNextEpisodeAutoPlay"] != true {
		t.Fatal("viewer changed owner's autoplay preference")
	}
}

func TestAccountRevocationAlsoInvalidatesSignedImages(t *testing.T) {
	s, _ := fixture(t)
	id, accessToken := createViewer(t, s)
	video := stableID("/volume1/video/Фильм.mkv")
	item := jsonBody(t, accountRequest(s, "GET", "/Items/"+video, "", accessToken))
	tag := item["ImageTags"].(map[string]any)["Primary"].(string)
	path := "/Items/" + video + "/Images/Primary?tag=" + tag
	if w := accountRequest(s, "GET", path, "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	change := `{"Username":"guest","Role":"viewer","AllowedRoots":[],"Disabled":false}`
	jsonBody(t, call(s, "POST", "/NAS/Users/"+id, change, true))
	if w := accountRequest(s, "GET", path, "", ""); w.Code == 200 {
		t.Fatal("old signed image URL bypassed revoked library access")
	}
	change = `{"Username":"guest","Role":"viewer","AllowedRoots":["video"],"Disabled":true}`
	jsonBody(t, call(s, "POST", "/NAS/Users/"+id, change, true))
	if w := accountRequest(s, "GET", "/Users/Me", "", accessToken); w.Code != 401 {
		t.Fatal("disabled account retained a valid session", w.Code)
	}
	if strings.Contains(call(s, "GET", "/Users/Public", "", false).Body.String(), "guest") {
		t.Fatal("disabled user is still selectable")
	}
}

func TestLastAdministratorAndLegacyLogin(t *testing.T) {
	s, _ := fixture(t)
	if w := call(s, "POST", "/NAS/Users/"+userID, `{"Username":"viewer","Role":"viewer","AllowedRoots":["video"]}`, true); w.Code != 409 {
		t.Fatal("last administrator could be removed", w.Code)
	}
	auth := jsonBody(t, call(s, "POST", "/Users/AuthenticateByName", `{"Username":"viewer","Pw":"123"}`, false))
	if auth["User"].(map[string]any)["Id"] != userID {
		t.Fatal("legacy owner identity changed")
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM tokens WHERE token=?", auth["AccessToken"]).Scan(&count); err != nil || count != 0 {
		t.Fatal("new session could be mistaken for legacy owner on rollback")
	}
}

func TestPasswordResetRevokesSessionAndSignedImage(t *testing.T) {
	s, _ := fixture(t)
	id, token := createViewer(t, s)
	video := stableID("/volume1/video/Фильм.mkv")
	item := jsonBody(t, accountRequest(s, "GET", "/Items/"+video, "", token))
	tag := item["ImageTags"].(map[string]any)["Primary"].(string)
	body := `{"Username":"guest","Password":"replacement","Role":"viewer","AllowedRoots":["video"]}`
	jsonBody(t, call(s, "POST", "/NAS/Users/"+id, body, true))
	if w := accountRequest(s, "GET", "/Users/Me", "", token); w.Code != 401 {
		t.Fatal("old session survived password reset")
	}
	if w := accountRequest(s, "GET", "/Items/"+video+"/Images/Primary?tag="+tag, "", ""); w.Code == 200 {
		t.Fatal("old image bearer link survived password reset")
	}
}
