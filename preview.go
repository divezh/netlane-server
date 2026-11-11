package webassets

import (
	"embed"
	"io/fs"
	"net/http"
)

// Preview contains only the explicitly labelled demo screens; no server credentials or runtime files.
//
//go:embed design/*.html design/style.css design/app.js design/icon.png
var preview embed.FS

//go:embed web/*
var application embed.FS

// The ordinary website the VPN endpoint shows to visitors without VPN credentials.
//
//go:embed decoy/*
var decoySite embed.FS

func Decoy() fs.FS {
	files, err := fs.Sub(decoySite, "decoy")
	if err != nil {
		panic(err)
	}
	return files
}

func App() http.Handler {
	files, err := fs.Sub(application, "web")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(files))
}

func Preview() http.Handler {
	files, err := fs.Sub(preview, "design")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(files))
}
