package main

import "testing"

func TestPlaybackPreferencesRoundTripAndPartialUpdate(t *testing.T) {
	s, _ := fixture(t)
	path := "/Users/" + userID + "/Configuration"
	if w := call(s, "POST", path, `{"AudioLanguagePreference":"en","SubtitleLanguagePreference":"ru","SubtitleMode":"Always","PlayDefaultAudioTrack":false}`, true); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	configuration := jsonBody(t, call(s, "GET", "/Users/"+userID, "", true))["Configuration"].(map[string]any)
	if configuration["SubtitleMode"] != "Always" || configuration["AudioLanguagePreference"] != "en" || configuration["PlayDefaultAudioTrack"] != false {
		t.Fatal(configuration)
	}
	if w := call(s, "POST", path, `{"EnableNextEpisodeAutoPlay":false}`, true); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if s.playbackConfiguration()["SubtitleMode"] != "Always" {
		t.Fatal("partial update discarded preferences")
	}
	if w := call(s, "POST", path, `{"SubtitleMode":"invalid"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	guestID, token := createViewer(t, s)
	guest := jsonBody(t, accountRequest(s, "GET", "/Users/"+guestID, "", token))
	if guest["Configuration"] != nil && guest["Configuration"].(map[string]any)["SubtitleMode"] == "Always" {
		t.Fatal("preferences leaked across accounts")
	}
}

func TestPreferredAudioAndSubtitleModes(t *testing.T) {
	s, _ := fixture(t)
	media := Media{Streams: []M{
		{
			"Type":      "Audio",
			"Index":     1,
			"Language":  "rus",
			"IsDefault": true,
		},
		{
			"Type":     "Audio",
			"Index":    2,
			"Language": "eng",
		},
		{
			"Type":     "Subtitle",
			"Index":    3,
			"Language": "eng",
			"Codec":    "subrip",
		},
		{
			"Type":     "Subtitle",
			"Index":    4,
			"Language": "rus",
			"Codec":    "subrip",
		},
		{
			"Type":     "Subtitle",
			"Index":    5,
			"Language": "rus",
			"Codec":    "subrip",
			"IsForced": true,
		},
	}}
	for _, test := range []struct {
		mode                    string
		audioLanguage           string
		defaultAudio            bool
		wantAudio, wantSubtitle int
	}{
		{"Always", "en", false, 2, 4},
		{"Smart", "en", false, 2, 5},
		{"OnlyForced", "en", false, 2, 5},
		{"None", "en", false, 2, -1},
		{"Always", "en", true, 1, 4},
	} {
		if err := s.savePlaybackConfiguration(M{
			"SubtitleMode":               test.mode,
			"SubtitleLanguagePreference": "ru",
			"AudioLanguagePreference":    test.audioLanguage,
			"PlayDefaultAudioTrack":      test.defaultAudio,
		}); err != nil {
			t.Fatal(err)
		}
		audio, subtitle := s.preferredStreams(media)
		if audio != test.wantAudio || subtitle != test.wantSubtitle {
			t.Fatalf("%s: got %d,%d", test.mode, audio, subtitle)
		}
	}
	// With no forced/default track, Always enables the preferred full track,
	// Smart enables it only for foreign audio, and None leaves it off.
	media.Streams = media.Streams[:4]
	for _, test := range []struct {
		mode, language string
		want           int
	}{{"Always", "en", 4}, {"Smart", "en", 4}, {"Smart", "ru", -1}, {"Default", "en", -1}, {"OnlyForced", "en", -1}} {
		if err := s.savePlaybackConfiguration(M{
			"SubtitleMode":            test.mode,
			"AudioLanguagePreference": test.language,
			"PlayDefaultAudioTrack":   false,
		}); err != nil {
			t.Fatal(err)
		}
		_, subtitle := s.preferredStreams(media)
		if subtitle != test.want {
			t.Fatalf("%s/%s: got %d", test.mode, test.language, subtitle)
		}
	}
}

func TestRememberedStreamsOverridePreferencesIncludingOff(t *testing.T) {
	s, _ := fixture(t)
	media := Media{
		ID: "remember-test",
		Streams: []M{
			{
				"Type":     "Audio",
				"Index":    1,
				"Language": "rus",
			},
			{
				"Type":     "Audio",
				"Index":    2,
				"Language": "eng",
			},
			{
				"Type":      "Subtitle",
				"Index":     3,
				"Language":  "rus",
				"Codec":     "subrip",
				"IsDefault": true,
			},
		},
	}
	audio, off := 2, -1
	if err := s.rememberStreams(media, &audio, &off); err != nil {
		t.Fatal(err)
	}
	source := s.source(media)
	if source["DefaultAudioStreamIndex"] != 2 || source["DefaultSubtitleStreamIndex"] != -1 {
		t.Fatal(source)
	}
	sub := 3
	if err := s.rememberStreams(media, nil, &sub); err != nil {
		t.Fatal(err)
	}
	if s.source(media)["DefaultAudioStreamIndex"] != 2 || s.source(media)["DefaultSubtitleStreamIndex"] != 3 {
		t.Fatal("partial report lost track selection")
	}
	invalid := 99
	if err := s.rememberStreams(media, nil, &invalid); err != nil {
		t.Fatal(err)
	}
	if s.source(media)["DefaultSubtitleStreamIndex"] != 3 {
		t.Fatal("invalid track persisted")
	}
	if err := s.savePlaybackConfiguration(M{"RememberSubtitleSelections": false}); err != nil {
		t.Fatal(err)
	}
	if _, exists := s.rememberedStreams(media)["SubtitleStreamIndex"]; exists {
		t.Fatal("remember disabled ignored")
	}
}
