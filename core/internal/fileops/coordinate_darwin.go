package fileops

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var coordinateTransfer = nativeCoordinatedTransfer

func (r *runner) coordinatedTransfer(src, dst string, move bool) (outcome, error) {
	return coordinateTransfer(r, src, dst, move)
}

func nativeCoordinatedTransfer(r *runner, src, dst string, move bool) (outcome, error) {
	path, overridden := os.LookupEnv("DFILES_NATIVE_HELPER")
	if !overridden {
		exe, err := os.Executable()
		if err != nil {
			return outcome{}, err
		}
		path = filepath.Join(filepath.Dir(exe), "dfiles-native")
	}
	if !filepath.IsAbs(path) {
		return outcome{}, fmt.Errorf("native platform helper requires an absolute path")
	}
	cmd := exec.CommandContext(r.job.ctx, path, "--fileops-coordinate")
	in, err := cmd.StdinPipe()
	if err != nil {
		return outcome{}, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return outcome{}, err
	}
	if err = cmd.Start(); err != nil {
		return outcome{}, err
	}
	if err = json.NewEncoder(in).Encode(map[string]any{"src": src, "dst": dst, "move": move}); err != nil {
		in.Close()
		cmd.Wait()
		return outcome{}, err
	}
	var lease struct {
		Src   string `json:"src"`
		Dst   string `json:"dst"`
		Error string `json:"error"`
	}
	err = json.NewDecoder(io.LimitReader(out, 1024*1024)).Decode(&lease)
	if err != nil || lease.Error != "" || !filepath.IsAbs(lease.Src) || !filepath.IsAbs(lease.Dst) || strings.ContainsRune(lease.Src, 0) || strings.ContainsRune(lease.Dst, 0) {
		in.Close()
		cmd.Wait()
		if lease.Error != "" {
			return outcome{}, fmt.Errorf("file coordination: %s", lease.Error)
		}
		if err != nil {
			return outcome{}, fmt.Errorf("file coordination: %w", err)
		}
		return outcome{}, fmt.Errorf("file coordination returned invalid paths")
	}
	done, transferErr := r.transfer(lease.Src, lease.Dst, move)
	in.Close()
	waitErr := cmd.Wait()
	if transferErr != nil {
		return done, transferErr
	}
	if waitErr != nil {
		return done, fmt.Errorf("file coordination: %w", waitErr)
	}
	return done, nil
}
