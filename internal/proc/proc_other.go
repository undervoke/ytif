//go:build !unix

package proc

import "os/exec"

func isolate(*exec.Cmd) {}

func reap(*exec.Cmd) {}
