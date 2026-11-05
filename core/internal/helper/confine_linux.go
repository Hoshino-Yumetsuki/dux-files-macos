package helper

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const keptCaps = 1<<unix.CAP_CHOWN | 1<<unix.CAP_DAC_OVERRIDE | 1<<unix.CAP_DAC_READ_SEARCH | 1<<unix.CAP_FOWNER

func confine(uid int) error {
	if err := allThreads(unix.SYS_PRCTL, unix.PR_SET_KEEPCAPS, 1, 0); err != nil {
		return fmt.Errorf("keepcaps: %w", err)
	}
	if err := syscall.Seteuid(uid); err != nil {
		return fmt.Errorf("seteuid: %w", err)
	}
	if err := allThreads(unix.SYS_SETFSUID, 0, 0, 0); err != nil {
		return fmt.Errorf("setfsuid: %w", err)
	}

	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{{Effective: keptCaps, Permitted: keptCaps}}
	if err := allThreads(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); err != nil {
		return fmt.Errorf("capset: %w", err)
	}
	runtime.KeepAlive(&header)
	runtime.KeepAlive(&data)

	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("dumpable: %w", err)
	}
	if err := allThreads(unix.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	return verifyConfined(uid)
}

func allThreads(trap, a1, a2, a3 uintptr) error {
	_, _, errno := syscall.AllThreadsSyscall(trap, a1, a2, a3)
	if errno != 0 {
		return errno
	}
	return nil
}

func verifyConfined(uid int) error {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	wantUID := "0\t" + strconv.Itoa(uid) + "\t0\t0"
	var gotUID, gotCaps, gotPermitted, gotNNP string
	for _, line := range strings.Split(string(status), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Uid":
			gotUID = value
		case "CapEff":
			gotCaps = value
		case "CapPrm":
			gotPermitted = value
		case "NoNewPrivs":
			gotNNP = value
		}
	}
	caps, parseErr := strconv.ParseUint(gotCaps, 16, 64)
	permitted, permErr := strconv.ParseUint(gotPermitted, 16, 64)
	if parseErr != nil || permErr != nil || caps != keptCaps || permitted != keptCaps || gotUID != wantUID || gotNNP != "1" {
		return errors.New("privilege drop did not take effect (uid " + gotUID + ", caps " + gotCaps + "/" + gotPermitted + ", nnp " + gotNNP + ")")
	}
	return nil
}
