package main

import (
	"os"
	"path/filepath"

	"github.com/duxlabs-dev/dux-go/log"
)

func init() {
	home, err := os.UserHomeDir()
	if err == nil {
		err = setDarwinXDGPaths(home)
	}
	if err != nil {
		log.Warnf("macOS data paths unavailable: %v", err)
	}
}

func setDarwinXDGPaths(home string) error {
	support := filepath.Join(home, "Library", "Application Support")
	for key, path := range map[string]string{
		"XDG_CONFIG_HOME": support,
		"XDG_DATA_HOME":   filepath.Join(support, "dfiles", "data"),
		"XDG_CACHE_HOME":  filepath.Join(home, "Library", "Caches"),
		"XDG_STATE_HOME":  filepath.Join(support, "state"),
	} {
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, path); err != nil {
			return err
		}
	}
	return nil
}
