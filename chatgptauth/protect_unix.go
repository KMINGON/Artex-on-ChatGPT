//go:build !windows

package chatgptauth

import "os"

func protectDirectory(path string) error { return os.Chmod(path, 0700) }
func protectFile(file *os.File) error    { return file.Chmod(0600) }
