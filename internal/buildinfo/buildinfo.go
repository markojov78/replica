package buildinfo

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
