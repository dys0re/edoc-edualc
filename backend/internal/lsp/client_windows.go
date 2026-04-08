//go:build windows

package lsp

import (
	"os/exec"
	"syscall"
)

func setSysProcAttrImpl(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
