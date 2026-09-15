package agentapi

import (
	"os/exec"
	"strconv"
)

func configureProcess(cmd *exec.Cmd) { cmd.Cancel = func() error { cleanupProcess(cmd); return nil } }
func cleanupProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	}
}
