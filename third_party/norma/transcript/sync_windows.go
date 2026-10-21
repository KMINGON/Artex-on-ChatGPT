//go:build windows

package transcript

// Windows does not expose directory fsync through os.File.Sync. File contents
// are still synced before publication; power-loss durability needs native tests.
func syncDirectory(string) error { return nil }
