package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func requireRegularFile(path string, limit int64) error {
	if path == "" || strings.ContainsRune(path, '\x00') {
		return errors.New("文件路径无效")
	}
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("目标不是普通非符号链接文件")
	}
	if info.Size() < 1 || info.Size() > limit {
		return errors.New("文件大小无效")
	}
	return nil
}

func rejectSymlinkComponents(path string) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return errors.New("文件路径必须是绝对路径")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("路径包含符号链接: %s", current)
		}
	}
	return nil
}

func hashFile(path string, limit int64) (string, error) {
	if err := requireRegularFile(path, limit); err != nil {
		return "", err
	}
	pathInfo, _ := os.Lstat(path)
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		return "", errors.New("文件读取期间身份发生变化")
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", errors.New("文件超过大小限制")
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func validDigestFile(path, expected string, limit int64) (bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	digest, err := hashFile(path, limit)
	if err != nil {
		return false, err
	}
	if digest != expected {
		return false, errors.New("已有文件摘要与预期不一致")
	}
	return true, nil
}

// acquireBinaryLock serializes every updater operation touching the live
// Runner binary. The lock is on a stable sidecar inode, so atomic rename of
// the binary cannot accidentally release it.
func acquireBinaryLock(binaryPath string) (*os.File, error) {
	if binaryPath == "" || !filepath.IsAbs(binaryPath) {
		return nil, errors.New("Runner 二进制锁路径无效")
	}
	directory := filepath.Dir(binaryPath)
	if err := rejectSymlinkComponents(directory); err != nil {
		return nil, err
	}
	lockPath := binaryPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开 Runner 二进制锁失败: %w", err)
	}
	if err := lock.Chmod(0o600); err != nil {
		lock.Close()
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, fmt.Errorf("获取 Runner 二进制锁失败: %w", err)
	}
	return lock, nil
}

func copyExclusive(source, target string, mode os.FileMode, expectedDigest string) error {
	if err := requireRegularFile(source, maxArtifactBytes); err != nil {
		return err
	}
	directory := filepath.Dir(target)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	if err := rejectSymlinkComponents(directory); err != nil {
		return err
	}
	input, sourceInfo, err := openStableSource(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	copyErr := copyAndSync(input, output, sourceInfo.Size(), maxArtifactBytes)
	if closeErr := output.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		if digest, hashErr := hashFile(target, maxArtifactBytes); hashErr != nil || digest != expectedDigest {
			copyErr = errors.New("回滚副本摘要校验失败")
		}
	}
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	return syncDirectory(directory)
}

func atomicReplace(source, target, expectedDigest, expectedTargetDigest string) error {
	if digest, err := hashFile(source, maxArtifactBytes); err != nil || digest != expectedDigest {
		return errors.New("待安装 Runner 制品摘要无效")
	}
	if err := requireRegularFile(target, maxArtifactBytes); err != nil {
		return err
	}
	targetInfo, err := os.Lstat(target)
	if err != nil {
		return err
	}
	directory := filepath.Dir(target)
	if err := rejectSymlinkComponents(directory); err != nil {
		return err
	}
	input, sourceInfo, err := openStableSource(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(directory, ".areasong-ops-runner-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := prepareReplacement(temporary, input, sourceInfo.Size(), targetInfo); err != nil {
		return err
	}
	if digest, err := hashFile(temporaryPath, maxArtifactBytes); err != nil || digest != expectedDigest {
		return errors.New("Runner 原子替换临时文件摘要无效")
	}
	// Re-check both inode and digest directly before rename. This closes the
	// TOCTOU window between the initial identity read and the destructive swap.
	currentInfo, err := os.Lstat(target)
	if err != nil || !os.SameFile(targetInfo, currentInfo) {
		return errors.New("Runner 目标二进制 inode 在替换前发生变化")
	}
	if expectedTargetDigest != "" {
		currentDigest, digestErr := hashFile(target, maxArtifactBytes)
		if digestErr != nil || currentDigest != expectedTargetDigest {
			return errors.New("Runner 目标二进制摘要在替换前发生变化")
		}
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func openStableSource(path string) (*os.File, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		file.Close()
		return nil, nil, errors.New("源文件读取期间身份发生变化")
	}
	return file, openedInfo, nil
}

func copyAndSync(input *os.File, output *os.File, size, limit int64) error {
	if size < 1 || size > limit {
		return errors.New("源文件大小无效")
	}
	written, err := io.Copy(output, io.LimitReader(input, limit+1))
	if err != nil {
		return err
	}
	if written != size || written > limit {
		return errors.New("源文件复制长度发生变化")
	}
	return output.Sync()
}

func prepareReplacement(
	temporary, input *os.File,
	size int64,
	targetInfo os.FileInfo,
) error {
	if err := copyAndSync(input, temporary, size, maxArtifactBytes); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(targetInfo.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if os.Geteuid() == 0 {
		stat, ok := targetInfo.Sys().(*syscall.Stat_t)
		if !ok || temporary.Chown(int(stat.Uid), int(stat.Gid)) != nil {
			temporary.Close()
			return errors.New("无法保留 Runner 二进制属主")
		}
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	return temporary.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
