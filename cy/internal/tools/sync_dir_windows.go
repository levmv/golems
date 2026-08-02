//go:build windows

package tools

func syncParentDir(string) error {
	// Go opens directories without the write access FlushFileBuffers requires
	// on Windows. The temporary file was already synced before rename, so skip
	// this Unix metadata-durability step instead of reporting failure after the
	// file has changed.
	return nil
}
