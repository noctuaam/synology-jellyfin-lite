package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageConfigurationPreservesStateReferences(t *testing.T) {
	root := t.TempDir()
	metadata := filepath.Join(root, "old-metadata.jsonl")
	os.WriteFile(metadata, []byte("metadata"), 0600)
	original := Config{
		Data:         filepath.Join(root, "existing-data"),
		MetadataFile: metadata,
		ServerID:     serverID,
		Listen:       "127.0.0.1:8097",
	}
	target := filepath.Join(root, "settings", "config.json")
	if err := preparePackageConfiguration(original, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var actual Config
	if err = json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Data != original.Data || actual.ServerID != original.ServerID || actual.MetadataFile == metadata {
		t.Fatal("migration changed state identity or retained obsolete metadata reference")
	}
	copied, err := os.ReadFile(actual.MetadataFile)
	if err != nil || string(copied) != "metadata" {
		t.Fatal(err)
	}
	if err := preparePackageConfiguration(original, target); err == nil {
		t.Fatal("existing configuration overwritten")
	}
}
