package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// preparePackageConfiguration moves configuration references out of an obsolete
// application directory without opening or changing its database. The caller
// must stop the service and back up state before installing the new package.
func preparePackageConfiguration(config Config, destination string) error {
	if !filepath.IsAbs(destination) {
		return errors.New("destination must be absolute")
	}
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	config.Web = "/var/packages/NASMediaFolders/target/web"
	if config.MetadataFile != "" {
		data, err := os.ReadFile(config.MetadataFile)
		if err != nil {
			return err
		}
		target := filepath.Join(directory, "video-metadata.jsonl")
		if _, err = os.Lstat(target); err == nil {
			return errors.New("metadata destination already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err = os.WriteFile(target, data, 0644); err != nil {
			return err
		}
		config.MetadataFile = target
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
