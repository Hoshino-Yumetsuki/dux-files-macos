package dbustest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow receive_sender="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>`

func Bus(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux D-Bus integration")
	}
	bin, err := exec.LookPath("dbus-daemon")
	switch {
	case err != nil && os.Getenv("CI") != "":
		t.Fatal("dbus-daemon not installed")
	case err != nil:
		t.Skip("dbus-daemon not installed")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "bus.conf")
	require.NoError(t, os.WriteFile(config, []byte(strings.Replace(busConfig, "%s", dir, 1)), 0o600))

	cmd := exec.Command(bin, "--config-file="+config, "--nofork", "--nopidfile", "--print-address")
	cmd.SysProcAttr = busProcessAttrs()
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	address, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	return strings.TrimSpace(address)
}

func Connect(t *testing.T, address string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(address)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
