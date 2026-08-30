package buildinfo

// Values are replaced with -ldflags during release builds.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info describes the running binary build.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Current returns the build metadata compiled into the binary.
func Current() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
	}
}
