// Package sqliteuri builds the path part of SQLite "file:" URIs from OS paths.
package sqliteuri

import (
	"path/filepath"
	"strings"
)

// Path returns the value for url.URL.Path in a SQLite "file:" URI that names
// osPath. A path without a volume name, which is every Linux and macOS path,
// is returned unchanged. A Windows drive path such as `D:\dir\x.db` becomes
// "/D:/dir/x.db", so url.URL.String gives "file:///D:/dir/x.db" instead of
// putting the drive into the URI authority; a UNC path such as
// `\\server\share\x.db` becomes "//server/share/x.db", giving
// "file:////server/share/x.db".
func Path(osPath string) string {
	return pathForVolume(osPath, filepath.VolumeName(osPath))
}

// pathForVolume is Path with the volume name supplied, so every branch can be
// tested on every OS.
func pathForVolume(osPath string, volume string) string {
	if volume == "" {
		return osPath
	}
	slashed := strings.ReplaceAll(osPath, `\`, "/")
	if len(volume) == 2 && volume[1] == ':' {
		return "/" + slashed
	}
	return slashed
}
