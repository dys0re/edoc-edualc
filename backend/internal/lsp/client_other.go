//go:build !windows

package lsp

import "os/exec"

func setSysProcAttrImpl(_ *exec.Cmd) {}
