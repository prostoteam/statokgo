package statok

// VersionString is the human-readable client version. Update this manually.
const VersionString = "20260303-004"

// Version returns the client version string.
func Version() string {
	return VersionString
}
