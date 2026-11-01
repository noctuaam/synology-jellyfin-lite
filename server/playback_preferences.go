package main

import (
	"encoding/json"
	"errors"
	"strings"
)

// playbackConfiguration is account scoped; updates merge with previous choices.
func (s *Server) playbackConfiguration() M {
	configuration := M{
		"AudioLanguagePreference":    "rus",
		"SubtitleLanguagePreference": "rus",
		"PlayDefaultAudioTrack":      true,
		"SubtitleMode":               "Default",
		"RememberAudioSelections":    true,
		"RememberSubtitleSelections": true,
		"EnableNextEpisodeAutoPlay":  true,
		"DisplayMissingEpisodes":     false,
		"GroupedFolders":             []string{},
		"MyMediaExcludes":            []string{},
		"OrderedViews":               []string{},
		"LatestItemsExcludes":        []string{},
		"HidePlayedInLatest":         false,
	}
	var raw string
	if s.db.QueryRow("SELECT body FROM prefs WHERE id=?", s.storageKey("userconfiguration")).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &configuration)
	}
	return configuration
}

func (s *Server) savePlaybackConfiguration(update M) error {
	configuration := s.playbackConfiguration()
	for _, key := range []string{"PlayDefaultAudioTrack", "RememberAudioSelections", "RememberSubtitleSelections", "EnableNextEpisodeAutoPlay"} {
		if value, exists := update[key]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("invalid boolean playback setting")
			}
			configuration[key] = value
		}
	}
	for _, key := range []string{"AudioLanguagePreference", "SubtitleLanguagePreference"} {
		if value, exists := update[key]; exists {
			language, ok := value.(string)
			if !ok || len(language) > 32 {
				return errors.New("invalid preferred language")
			}
			configuration[key] = strings.TrimSpace(language)
		}
	}
	if value, exists := update["SubtitleMode"]; exists {
		switch value {
		case "Default", "Smart", "Always", "OnlyForced", "None":
			configuration["SubtitleMode"] = value
		default:
			return errors.New("invalid subtitle mode")
		}
	}
	raw, err := json.Marshal(configuration)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO prefs VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", s.storageKey("userconfiguration"), string(raw))
	return err
}

func normalizedLanguage(value any) string {
	language, _ := value.(string)
	language = strings.ToLower(strings.Split(strings.ReplaceAll(language, "_", "-"), "-")[0])
	switch language {
	case "ru":
		return "rus"
	case "en":
		return "eng"
	case "fr", "fre":
		return "fra"
	case "de", "ger":
		return "deu"
	case "es":
		return "spa"
	case "it":
		return "ita"
	case "ja":
		return "jpn"
	case "zh", "chi":
		return "zho"
	case "ko":
		return "kor"
	case "pt":
		return "por"
	case "uk":
		return "ukr"
	}
	return language
}

func (s *Server) preferredStreams(media Media) (int, int) {
	configuration := s.playbackConfiguration()
	audio, defaultAudio, preferredAudio := -1, -1, -1
	audioLanguage := normalizedLanguage(configuration["AudioLanguagePreference"])
	subtitleLanguage := normalizedLanguage(configuration["SubtitleLanguagePreference"])
	for _, stream := range media.Streams {
		if stream["Type"] != "Audio" {
			continue
		}
		index := streamIndex(stream)
		if audio < 0 {
			audio = index
		}
		if stream["IsDefault"] == true && defaultAudio < 0 {
			defaultAudio = index
		}
		if audioLanguage != "" && normalizedLanguage(stream["Language"]) == audioLanguage && preferredAudio < 0 {
			preferredAudio = index
		}
	}
	if configuration["PlayDefaultAudioTrack"] == true && defaultAudio >= 0 {
		audio = defaultAudio
	} else if preferredAudio >= 0 {
		audio = preferredAudio
	} else if defaultAudio >= 0 {
		audio = defaultAudio
	}
	selectedAudioLanguage := ""
	for _, stream := range media.Streams {
		if stream["Type"] == "Audio" && streamIndex(stream) == audio {
			selectedAudioLanguage = normalizedLanguage(stream["Language"])
		}
	}
	mode, _ := configuration["SubtitleMode"].(string)
	if mode == "None" {
		return audio, -1
	}
	best, bestScore := -1, -1
	for _, stream := range media.Streams {
		if stream["Type"] != "Subtitle" {
			continue
		}
		forced := stream["IsForced"] == true
		matches := subtitleLanguage != "" && normalizedLanguage(stream["Language"]) == subtitleLanguage
		isDefault := stream["IsDefault"] == true
		eligible := forced
		switch mode {
		case "Always":
			eligible = true
		case "OnlyForced":
			eligible = forced
		case "Smart":
			eligible = forced || (matches && selectedAudioLanguage != subtitleLanguage)
		default:
			eligible = forced || isDefault
		}
		if !eligible {
			continue
		}
		score := 0
		if matches {
			score += 8
		}
		if isDefault {
			score += 4
		}
		if forced && mode != "Always" {
			score += 2
		}
		if !forced && mode == "Always" {
			score += 2
		}
		if textSubtitle(stream) {
			score++
		}
		if score > bestScore {
			best, bestScore = streamIndex(stream), score
		}
	}
	return audio, best
}

// rememberedStreams validates indices against current metadata so replacing a
// file never applies a stale stream index to an unrelated track.
func (s *Server) rememberedStreams(media Media) map[string]int {
	result := map[string]int{}
	var raw string
	if s.db.QueryRow("SELECT body FROM prefs WHERE id=?", s.storageKey("streams:"+media.ID)).Scan(&raw) != nil {
		return result
	}
	var saved map[string]int
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return result
	}
	configuration := s.playbackConfiguration()
	for _, kind := range []string{"Audio", "Subtitle"} {
		if configuration["Remember"+kind+"Selections"] != true {
			continue
		}
		key := kind + "StreamIndex"
		index, exists := saved[key]
		if !exists {
			continue
		}
		if kind == "Subtitle" && index == -1 {
			result[key] = index
			continue
		}
		for _, stream := range media.Streams {
			if stream["Type"] == kind && streamIndex(stream) == index {
				result[key] = index
				break
			}
		}
	}
	return result
}

func (s *Server) rememberStreams(media Media, audio, subtitle *int) error {
	saved := s.rememberedStreams(media)
	changed := false
	configuration := s.playbackConfiguration()
	for _, selection := range []struct {
		kind  string
		index *int
	}{{"Audio", audio}, {"Subtitle", subtitle}} {
		if selection.index == nil || configuration["Remember"+selection.kind+"Selections"] != true {
			continue
		}
		index := *selection.index
		valid := selection.kind == "Subtitle" && index == -1
		for _, stream := range media.Streams {
			if stream["Type"] == selection.kind && streamIndex(stream) == index {
				valid = true
			}
		}
		key := selection.kind + "StreamIndex"
		if old, exists := saved[key]; valid && (!exists || old != index) {
			saved[key] = index
			changed = true
		}
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO prefs VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", s.storageKey("streams:"+media.ID), string(raw))
	return err
}
