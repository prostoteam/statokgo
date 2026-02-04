package statok

// VersionString is the human-readable client version. Update this manually.
const VersionString = "20260204-003"

// Version returns the client version string.
func Version() string {
	return VersionString
}
