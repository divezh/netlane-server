//go:build !windows

package config

import (
	"os"
	"syscall"
)

// keepOwner gives a replacement file the owner of the original, so a root
// "passwd" run does not leave the service unable to read its own config.
func keepOwner(f *os.File, original os.FileInfo) error {
	st, ok := original.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return nil
	}
	return f.Chown(int(st.Uid), int(st.Gid))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
