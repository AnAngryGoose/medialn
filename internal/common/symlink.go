package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HostToContainer translates a host-side absolute path to its container
// equivalent by replacing the host root prefix with the container root.
// This is what gets stored as the symlink target so that links resolve
// correctly inside Jellyfin/arr containers.
// Returns an error if path does not start with hostRoot.
func HostToContainer(path, hostRoot, containerRoot string) (string, error) {
	if !strings.HasPrefix(path, hostRoot) {
		return "", fmt.Errorf("path %s does not start with host root %s", path, hostRoot)
	}
	return containerRoot + path[len(hostRoot):], nil
}

// ContainerToHost translates a container-side absolute path to its host
// equivalent by replacing the container root prefix with the host root.
// Returns the path unchanged if it does not start with containerRoot
// or if either root is empty.
func ContainerToHost(path, hostRoot, containerRoot string) string {
	if hostRoot == "" || containerRoot == "" || !strings.HasPrefix(path, containerRoot) {
		return path
	}
	return hostRoot + path[len(containerRoot):]
}

// MakeSymlink creates a symlink at linkPath pointing to targetHostPath
// (translated to container coordinates). Returns true if the symlink was
// created, false if it already existed (skip) or an error occurred.
//
// linkPath must be a SafePath to enforce that we never write to source dirs.
// In dry-run mode the symlink is not created but true is still returned to
// indicate it would have been.
func MakeSymlink(linkPath SafePath, targetHostPath string, dryRun bool, hostRoot, containerRoot string) bool {
	p := linkPath.Path()
	if _, err := os.Lstat(p); err == nil {
		// Already exists (symlink or otherwise) — skip.
		return false
	}
	if dryRun {
		return true
	}
	containerTarget, err := HostToContainer(targetHostPath, hostRoot, containerRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] MakeSymlink: %v\n", err)
		return false
	}
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] MakeSymlink: mkdir %s: %v\n", filepath.Dir(p), err)
		return false
	}
	if err := Symlink(containerTarget, linkPath); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] MakeSymlink: symlink %s -> %s: %v\n", p, containerTarget, err)
		return false
	}
	return true
}

// EnsureDir creates the directory (and parents) at path unless dry-run.
func EnsureDir(path SafePath, dryRun bool) error {
	if dryRun {
		return nil
	}
	return MkdirAll(path, 0o755)
}

// SymlinkTargetExists returns true if the symlink at linkPath has a
// resolvable target. Non-symlinks always return true (they exist).
// Container-to-host path translation is applied when resolving.
func SymlinkTargetExists(linkPath, hostRoot, containerRoot string) bool {
	if _, err := os.Lstat(linkPath); err != nil {
		return false
	}
	if !IsSymlink(linkPath) {
		return true
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		return false
	}
	// Translate container path back to host for stat check.
	target = ContainerToHost(target, hostRoot, containerRoot)
	_, err = os.Stat(target)
	return err == nil
}

// IsSymlink reports whether path is a symbolic link.
func IsSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// IsBareDir reports whether path is a real directory (not a symlink).
func IsBareDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// CleanBrokenSymlinks removes broken symlinks under directory and prunes
// any directories that become empty as a result. Returns count removed.
//
// directory must be a SafePath — this function only operates on output dirs.
func CleanBrokenSymlinks(directory SafePath, hostRoot, containerRoot string) (int, error) {
	dir := directory.Path()
	removed := 0

	// First pass: remove broken file symlinks.
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.Type()&os.ModeSymlink != 0 {
			if !SymlinkTargetExists(path, hostRoot, containerRoot) {
				sp, err := NewSafePath(path, []string{dir})
				if err != nil {
					return fmt.Errorf("refusing to remove %s: not under output root", path)
				}
				if rerr := Remove(sp); rerr == nil {
					removed++
				}
			}
		}
		return nil
	})
	if err != nil {
		return removed, err
	}

	// Second pass: prune empty directories (bottom-up).
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if d.IsDir() {
			entries, rerr := os.ReadDir(path)
			if rerr == nil && len(entries) == 0 {
				sp, err := NewSafePath(path, []string{dir})
				if err != nil {
					return nil
				}
				Remove(sp) // best-effort
			}
		}
		return nil
	})
	return removed, err
}

// ValidateOutputDir checks a directory for real (non-symlink) video files.
// Returns count found. If count > 0 and not dry-run, prompts the user.
// When nonInteractive is true, logs a warning and continues without prompting.
// Returns an error if the user declines to continue.
func ValidateOutputDir(directory string, dryRun, nonInteractive bool) (int, error) {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return 0, nil
	}
	var real []string
	filepath.WalkDir(directory, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && IsVideo(d.Name()) && !IsSymlink(path) {
			real = append(real, path)
		}
		return nil
	})
	if len(real) == 0 {
		return 0, nil
	}
	fmt.Printf("\n[WARNING] %d real video file(s) in output dir:\n", len(real))
	limit := 10
	if len(real) < limit {
		limit = len(real)
	}
	for _, f := range real[:limit] {
		fmt.Printf("  %s\n", f)
	}
	if len(real) > 10 {
		fmt.Printf("  ... (%d more)\n", len(real)-10)
	}
	fmt.Println("\n  Output dirs should only contain symlinks.")
	fmt.Println("  If this IS your real library, your config is wrong.")
	if nonInteractive {
		fmt.Print("  (non-interactive, continuing)\n\n")
		return len(real), nil
	}
	if dryRun {
		fmt.Print("  (dry-run, continuing)\n\n")
		return len(real), nil
	}
	fmt.Print("\n")
	c := PromptChoice("  Continue anyway? [y/N] ", []string{"y", "n", ""})
	if c == "y" {
		return len(real), nil
	}
	return len(real), fmt.Errorf("aborted by user")
}
