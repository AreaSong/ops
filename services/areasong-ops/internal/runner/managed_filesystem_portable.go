//go:build !linux

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func openManagedNode(root, cleanPath string) (*os.File, os.FileInfo, error) {
	target := filepath.Join(root, filepath.FromSlash(cleanPath))
	before, err := os.Lstat(target)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("受管文件路径包含符号链接")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, nil, errors.New("受管文件在打开期间身份已变化")
	}
	return file, after, nil
}

func replaceManagedFile(
	root, cleanPath, content, expectedDigest, resultingDigest string,
	expected os.FileInfo,
) (bool, error) {
	target := filepath.Join(root, filepath.FromSlash(cleanPath))
	temporary, err := writeManagedReplacementPortable(target, content, expected)
	if err != nil {
		return false, err
	}
	defer os.Remove(temporary)
	current, info, err := openManagedNode(root, cleanPath)
	if err != nil {
		return false, errors.New("文件目标在替换前身份失效")
	}
	currentContent, readErr := readManagedTextFile(current, expected.Size())
	closeErr := current.Close()
	if readErr != nil || closeErr != nil || !os.SameFile(expected, info) {
		return false, errors.New("文件目标在替换前 inode 已变化")
	}
	if digestText(currentContent) != expectedDigest {
		return false, errors.New("文件目标在替换前摘要已变化")
	}
	if err := os.Rename(temporary, target); err != nil {
		return false, err
	}
	if err := managedSyncDirectory(filepath.Dir(target)); err != nil {
		return true, err
	}
	return verifyManagedReplacement(root, cleanPath, resultingDigest, int64(len(content)))
}

func verifyManagedReplacement(root, cleanPath, digest string, limit int64) (bool, error) {
	file, info, err := openManagedNode(root, cleanPath)
	if err != nil || !info.Mode().IsRegular() {
		return true, errors.New("文件原子替换后的身份核验失败")
	}
	defer file.Close()
	content, err := readManagedTextFile(file, limit)
	if err != nil || digestText(content) != digest {
		return true, errors.New("文件原子替换后的摘要核验失败")
	}
	return true, nil
}

func readManagedBackup(path string, limit int64) (string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("文件回滚副本不可用")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return "", errors.New("文件回滚副本身份已变化")
	}
	return readManagedTextFile(file, limit)
}

func writeManagedReplacementPortable(
	target, content string,
	original os.FileInfo,
) (string, error) {
	directory := filepath.Dir(target)
	file, err := os.CreateTemp(directory, ".areasong-ops-file-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	clean := func(result error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", result
	}
	if err := file.Chmod(original.Mode().Perm()); err != nil {
		return clean(err)
	}
	if stat, ok := original.Sys().(*syscall.Stat_t); ok {
		if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return clean(err)
		}
	}
	if _, err := file.WriteString(content); err != nil {
		return clean(err)
	}
	if err := file.Sync(); err != nil {
		return clean(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
