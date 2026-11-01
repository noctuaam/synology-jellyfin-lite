package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyPhotoPlayerPatchIsPinnedAndLeavesModernPlayerAvailable(t *testing.T) {
	const asset = "photoPlayer-plugin.d901b9200b6b6a1e1abf.chunk.js"
	original, err := os.ReadFile(filepath.Join("..", "web-dist", asset))
	if os.IsNotExist(err) {
		t.Skip("official client fixture not present")
	}
	if err != nil {
		t.Fatal(err)
	}
	patched, err := patchQueueAsset(asset, original)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"NASLegacyPhotoViewer(t)", "nasChrome[1])<70", "return new Promise", "Web0S|WebOS|SmartTV"} {
		if !strings.Contains(string(patched), required) {
			t.Fatal("missing compatibility guard", required)
		}
	}
	if _, err = patchQueueAsset(asset, []byte("unexpected plugin")); err == nil {
		t.Fatal("unrecognized plugin was patched")
	}
	s, _ := fixture(t)
	s.cfg.Web = filepath.Join("..", "web-dist")
	response := call(s, "GET", "/web/"+asset, "", false)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "NASLegacyPhotoViewer(t)") {
		t.Fatal("patched plugin not routed", response.Code)
	}
	response = call(s, "GET", "/nas-legacy-photo.js", "", false)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "window.NASLegacyPhotoViewer") {
		t.Fatal("viewer script unavailable", response.Code)
	}
}
