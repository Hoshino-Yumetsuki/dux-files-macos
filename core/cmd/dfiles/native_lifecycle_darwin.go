package main

import (
	"errors"
	"os"
	"strconv"
)

func trackNativeUI() (func(), error) {
	raw := os.Getenv("DFILES_UI_PID")
	if raw == "" {
		return nil, nil
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 1 || pid != os.Getppid() {
		return nil, errors.New("DFILES_UI_PID must identify the native UI parent process")
	}
	return shellApp.TrackExternalUI(pid)
}

func watchNativeUIStdin(enabled bool) <-chan struct{} {
	if !enabled {
		return nil
	}
	done := make(chan struct{})
	go func() {
		var buf [1]byte
		for {
			if _, err := os.Stdin.Read(buf[:]); err != nil {
				close(done)
				return
			}
		}
	}()
	return done
}
