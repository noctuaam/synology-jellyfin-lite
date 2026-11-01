package main

import (
	"net/url"
	"testing"
)

func TestGlobalSearchSections(t *testing.T) {
	s, _ := fixture(t)
	// Jellyfin's search hook awaits these collections before requesting videos
	// and photos. A 404 here discards the entire search, including valid hits.
	for _, path := range []string{"/Persons", "/Artists", "/Artists/AlbumArtists"} {
		if w := call(s, "GET", path, "", false); w.Code != 401 {
			t.Fatal("search category bypassed authentication", path, w.Code)
		}
		response := jsonBody(t, call(s, "GET", path+"?SearchTerm=test", "", true))
		if response["TotalRecordCount"] != float64(0) || len(response["Items"].([]any)) != 0 {
			t.Fatal("folder index should not invent people or artist collections", path, response)
		}
	}
	for _, test := range []struct{ kind, term string }{
		{"Video", "  ФиЛьМ  "},
		{"Audio", "TRACK"},
		{"Photo", "фОТО"},
	} {
		query := url.Values{
			"Recursive":  {"true"},
			"MediaTypes": {test.kind},
			"SearchTerm": {test.term},
		}
		response := jsonBody(t, call(s, "GET", "/Items?"+query.Encode(), "", true))
		if response["TotalRecordCount"] != float64(1) || len(response["Items"].([]any)) != 1 {
			t.Fatal("missing search result", test, response)
		}
	}
}
