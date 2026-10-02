package buildinfo

import (
	"fmt"
	"io"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
	}
}

func PrintVersion(args []string, output io.Writer) bool {
	if len(args) != 1 || (args[0] != "--version" && args[0] != "-v") {
		return false
	}

	info := Get()
	fmt.Fprintf(output, "Replica\nVersion: %s\nCommit: %s\nBuild date: %s\n", info.Version, info.Commit, info.BuildDate)
	return true
}
