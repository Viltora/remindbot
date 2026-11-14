// Package version holds the build version, set at build time:
//
//	go build -ldflags "-X remindbot/internal/version.Version=20261017-1930"
package version

var Version = "dev"
