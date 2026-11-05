package main

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/spf13/cobra"
)

func init() {
	for _, cmd := range rootCmd.Commands() {
		switch cmd.Name() {
		case "kill":
			cmd.Run = nil
			cmd.RunE = func(*cobra.Command, []string) error { return stopNativeUI() }
		case "restart":
			cmd.RunE = func(*cobra.Command, []string) error {
				if err := stopNativeUI(); err != nil {
					return err
				}
				return shellApp.RunDaemon(false)
			}
		}
	}
}

func stopNativeUI() error {
	socket, err := shellApp.UISocketPath()
	if err != nil {
		shellApp.Kill()
		return nil
	}
	client, err := ipc.Dial(socket)
	if err != nil {
		shellApp.Kill()
		return nil
	}
	resp, err := client.Call(ipc.Request{ID: 1, Method: "ui.quit"})
	client.Close()
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err != nil {
			shellApp.Kill()
			return nil
		}
		conn.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out stopping the Dux Files backend")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
