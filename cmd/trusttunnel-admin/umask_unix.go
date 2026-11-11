//go:build !windows

package main

import "syscall"

func secureUmask() { syscall.Umask(0077) }
