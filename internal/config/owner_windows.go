package config

import "os"

func keepOwner(*os.File, os.FileInfo) error { return nil }

func syncDir(string) error { return nil }
