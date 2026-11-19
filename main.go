package main

import (
	"flag"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"codeberg.org/unixery/buildinfo"

	"lyrunderground/internal/appinfo"
	"lyrunderground/internal/ui"
)

func main() {
	flagVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *flagVersion {
		fmt.Printf("%s %s (git sha %s)\n", appinfo.NAME, displayVersion(), buildinfo.GitSha)
		return
	}
	fmt.Printf("%s %s\n", appinfo.NAME, displayVersion())

	a := app.NewWithID("de.thomue." + appinfo.NAME)
	window := a.NewWindow(fmt.Sprintf("%s %s – LRCLIB Lyrics Uploader", appinfo.APP_NAME, displayVersion()))
	window.Resize(fyne.NewSize(1180, 820))
	ui.New(window)
	window.ShowAndRun()
}

func displayVersion() string {
	if buildinfo.GitTag == "" || buildinfo.GitTag == "n/a" {
		return "dev"
	}
	return buildinfo.GitTag
}
