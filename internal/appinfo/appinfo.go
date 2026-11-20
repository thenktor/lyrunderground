// Package appinfo contains the application's public and technical names.
package appinfo

import "codeberg.org/unixery/buildinfo"

const (
	// APP_NAME is the human readable application name.
	APP_NAME = "Lyrics From The Underground"

	// NAME is used for technical identifiers such as the app ID and User-Agent.
	NAME = "lyrunderground"
)

// Version returns the version from the linker metadata. Builds without
// injected information (go run, plain go build) report "dev".
func Version() string {
	if buildinfo.GitTag == "" || buildinfo.GitTag == "n/a" {
		return "dev"
	}
	return buildinfo.GitTag
}
