package encryption

// The CLI interoperability check builds the official age command from the
// pinned module. Keep its optional SSH and tag recipient packages in the test
// module graph so CI can build that command using the repository's Go version.
import (
	_ "filippo.io/age/agessh"
	_ "filippo.io/age/tag"
)
