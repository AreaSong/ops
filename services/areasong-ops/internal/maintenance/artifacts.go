package maintenance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const uuidExpression = `[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}`

var (
	operationNamePattern = regexp.MustCompile(`^(?:` + uuidExpression + `|update_[0-9]+_` + uuidExpression + `)$`)
	snapshotNamePattern  = regexp.MustCompile(`^ops-[0-9]{8}T[0-9]{6}Z\.db$`)
	sensitiveNamePattern = regexp.MustCompile(`^http-[A-Za-z0-9._-]+\.json$`)
)

type PruneResult struct {
	OperationDirectories int
	Snapshots            int
	SensitiveFiles       int
}

func PruneArtifacts(
	stateRoot, legacyStateRoot string,
	retention time.Duration,
	now time.Time,
	protectedOperationIDs map[string]struct{},
) (PruneResult, error) {
	var result PruneResult
	if retention <= 0 {
		return result, errors.New("artifact retention must be positive")
	}
	roots := []string{stateRoot}
	if legacyStateRoot != "" && filepath.Clean(legacyStateRoot) != filepath.Clean(stateRoot) {
		roots = append(roots, legacyStateRoot)
	}
	for _, root := range roots {
		if err := validateRoot(root); err != nil {
			return result, err
		}
		removedDirectories, removedSensitive, err := pruneOperations(
			filepath.Join(root, "operations"), now.Add(-retention), protectedOperationIDs,
		)
		result.OperationDirectories += removedDirectories
		result.SensitiveFiles += removedSensitive
		if err != nil {
			return result, err
		}
	}
	removedSnapshots, err := pruneSnapshots(filepath.Join(stateRoot, "snapshots"), now.Add(-retention))
	result.Snapshots += removedSnapshots
	return result, err
}

func validateRoot(path string) error {
	clean := filepath.Clean(path)
	if path == "" || !filepath.IsAbs(clean) || clean == string(filepath.Separator) {
		return fmt.Errorf("unsafe artifact root: %q", path)
	}
	info, err := os.Lstat(clean)
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("artifact root is unsafe: %s", clean)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func pruneOperations(
	root string,
	cutoff time.Time,
	protectedOperationIDs map[string]struct{},
) (int, int, error) {
	entries, err := readSecureDirectory(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	removedDirectories, removedSensitive := 0, 0
	for _, entry := range entries {
		if !operationNamePattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return removedDirectories, removedSensitive, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return removedDirectories, removedSensitive, fmt.Errorf("unsafe operation artifact: %s", path)
		}
		originalModTime := info.ModTime()
		count, err := scrubSensitiveFiles(path)
		removedSensitive += count
		if err != nil {
			return removedDirectories, removedSensitive, err
		}
		if _, protected := protectedOperationIDs[entry.Name()]; protected {
			continue
		}
		if originalModTime.Before(cutoff) {
			if err := os.RemoveAll(path); err != nil {
				return removedDirectories, removedSensitive, err
			}
			removedDirectories++
		}
	}
	return removedDirectories, removedSensitive, nil
}

func scrubSensitiveFiles(operation string) (int, error) {
	entries, err := os.ReadDir(operation)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if name != "sub2api.env.before" && name != "health.json" && !sensitiveNamePattern.MatchString(name) {
			continue
		}
		path := filepath.Join(operation, name)
		info, err := os.Lstat(path)
		if err != nil {
			return removed, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return removed, fmt.Errorf("unsafe sensitive artifact: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func pruneSnapshots(root string, cutoff time.Time) (int, error) {
	entries, err := readSecureDirectory(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !snapshotNamePattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return removed, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return removed, fmt.Errorf("unsafe snapshot artifact: %s", path)
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func readSecureDirectory(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact directory is unsafe: %s", path)
	}
	return os.ReadDir(path)
}
