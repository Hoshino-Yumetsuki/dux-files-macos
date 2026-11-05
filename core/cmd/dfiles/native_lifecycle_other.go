//go:build !darwin

package main

func trackNativeUI() (func(), error) { return nil, nil }

func watchNativeUIStdin(bool) <-chan struct{} { return nil }
