package main

import (
	"strings"
	"testing"
)

func TestVideoNavigationScriptIsPublicButReadOnly(t *testing.T) {
	s, _ := fixture(t)
	w := call(s, "GET", "/nas-video-navigation.js", "", false)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatal(w.Code)
	}
	if w = call(s, "POST", "/nas-video-navigation.js", "", false); w.Code != 405 {
		t.Fatal("script accepted a mutation", w.Code)
	}
}

func TestVideoQueueDoesNotIncludeOtherFolders(t *testing.T) {
	s, _ := fixture(t)
	response := jsonBody(t, call(s, "GET", "/Items?ParentId="+libraryID+"&Recursive=false&MediaTypes=Video&SortBy=SortName&NasPlaybackQueue=true", "", true))
	items := response["Items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["Name"] != "Фильм" {
		t.Fatal("queue crossed into another folder", response)
	}
}
