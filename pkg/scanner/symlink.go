package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func allowFilesystemEntry(root, path string, entry os.DirEntry, followSymlinks bool) (bool, error) {
	if entry.Type()&os.ModeSymlink == 0 {
		return true, nil
	}
	if !followSymlinks {
		return false, nil
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false, err
	}
	resolvedPath, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return false, err
	}
	if err := requirePathInsideRoot(resolvedRoot, resolvedPath); err != nil {
		return false, err
	}
	return true, nil
}

func requirePathInsideRoot(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("resolved symlink path escapes root: %s", candidate)
	}
	return nil
}
