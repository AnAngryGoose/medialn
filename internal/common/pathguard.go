// Package common provides shared utilities for medialnk.
// pathguard.go enforces the core invariant: source files are never written to.
//
// SafePath is an opaque type whose only constructor (NewSafePath) validates
// that the path is under a registered output root. All filesystem write
// functions in this package accept only SafePath, never raw strings.
// This is a compile-time constraint — bypassing it requires a code change,
// not just a runtime mistake.
package common

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SafePath is an opaque wrapper around a validated output path.
// The unexported field means it cannot be constructed outside NewSafePath.
type SafePath struct {
	path string
}

// NewSafePath validates that p is under at least one of the given outputRoots
// and returns a SafePath if so. Returns an error if p is not under any root.
//
// outputRoots must be absolute, clean paths.
func NewSafePath(p string, outputRoots []string) (SafePath, error) {
	p = filepath.Clean(p)
	for _, root := range outputRoots {
		root = filepath.Clean(root)
		// Require the path to be under the root, not just share a prefix.
		// E.g. root=/out should not match /output.
		if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
			return SafePath{path: p}, nil
		}
	}
	return SafePath{}, fmt.Errorf("path %q is not under any output root %v", p, outputRoots)
}

// Path returns the validated path string. Use this for read operations
// (stat, lstat, readdir) where no write occurs.
func (sp SafePath) Path() string {
	return sp.path
}

// String implements fmt.Stringer.
func (sp SafePath) String() string {
	return sp.path
}

// --- Write functions — only these may perform filesystem mutations ---

// Symlink creates a symlink at link pointing to target.
// target is the symlink content (the path the link points to) — typically a
// container path produced by HostToContainer. link must be a SafePath.
func Symlink(target string, link SafePath) error {
	return os.Symlink(target, link.path)
}

// Remove removes the named file or (empty) directory.
// Only accepts a SafePath — cannot be called with a raw string.
func Remove(path SafePath) error {
	return os.Remove(path.path)
}

// MkdirAll creates path and any necessary parents, like os.MkdirAll.
func MkdirAll(path SafePath, perm fs.FileMode) error {
	return os.MkdirAll(path.path, perm)
}

// WriteFile writes data to path, like os.WriteFile.
func WriteFile(path SafePath, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path.path, data, perm)
}
