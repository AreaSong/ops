//go:build linux

package runner

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const managedResolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

func openManagedNode(root, cleanPath string) (*os.File, os.FileInfo, error) {
	rootFD, err := openManagedRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer unix.Close(rootFD)
	fd, err := openManagedAt(rootFD, cleanPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW)
	if err != nil {
		return nil, nil, fmt.Errorf("openat2 打开受管路径失败: %w", err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root, filepath.FromSlash(cleanPath)))
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if err := validateManagedOpenedInfo(info); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func replaceManagedFile(
	root, cleanPath, content, expectedDigest, resultingDigest string,
	expected os.FileInfo,
) (bool, error) {
	rootFD, err := openManagedRoot(root)
	if err != nil {
		return false, err
	}
	defer unix.Close(rootFD)
	parentPath, base := path.Dir(cleanPath), path.Base(cleanPath)
	if cleanPath == "" || base == "." || base == "/" {
		return false, errors.New("受管文件目标不能为空")
	}
	parentFD, err := openManagedAt(rootFD, parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW)
	if err != nil {
		return false, fmt.Errorf("openat2 打开受管父目录失败: %w", err)
	}
	defer unix.Close(parentFD)
	if err := verifyManagedTargetAt(parentFD, base, expected, expectedDigest); err != nil {
		return false, err
	}
	temporaryName, temporaryFD, err := createManagedTemporary(parentFD)
	if err != nil {
		return false, err
	}
	defer unix.Unlinkat(parentFD, temporaryName, 0)
	if err := writeManagedTemporary(temporaryFD, content, expected); err != nil {
		return false, err
	}
	if err := verifyManagedTargetAt(parentFD, base, expected, expectedDigest); err != nil {
		return false, err
	}
	if err := unix.Renameat(parentFD, temporaryName, parentFD, base); err != nil {
		return false, err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return true, err
	}
	return verifyManagedReplacement(root, cleanPath, resultingDigest)
}

func openManagedRoot(root string) (int, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, errors.New("文件白名单根路径必须是目录")
	}
	if stat.Mode&0o022 != 0 || (os.Geteuid() == 0 && stat.Uid != 0) {
		_ = unix.Close(fd)
		return -1, errors.New("文件白名单根路径权限或属主不安全")
	}
	return fd, nil
}

func openManagedAt(rootFD int, cleanPath string, flags int) (int, error) {
	if cleanPath == "" || cleanPath == "." {
		return unix.Dup(rootFD)
	}
	return unix.Openat2(rootFD, cleanPath, &unix.OpenHow{
		Flags: uint64(flags), Resolve: managedResolveFlags,
	})
}

func validateManagedOpenedInfo(info os.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("受管文件路径组件不能由 group/other 写入")
	}
	if os.Geteuid() == 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errors.New("受管文件路径组件必须由 root 拥有")
		}
	}
	return nil
}

func verifyManagedTargetAt(
	parentFD int,
	name string,
	expected os.FileInfo,
	expectedDigest string,
) error {
	fd, err := openManagedAt(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW)
	if err != nil {
		return errors.New("文件目标在替换前身份失效")
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return errors.New("文件目标在替换前 inode 已变化")
	}
	content, err := readManagedTextFile(file, expected.Size())
	if err != nil || digestText(content) != expectedDigest {
		return errors.New("文件目标在替换前摘要已变化")
	}
	return nil
}

func createManagedTemporary(parentFD int) (string, int, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, err
		}
		name := ".areasong-ops-file-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(parentFD, name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == nil {
			return name, fd, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", -1, err
		}
	}
	return "", -1, errors.New("无法创建受管文件原子替换临时文件")
}

func writeManagedTemporary(fd int, content string, original os.FileInfo) error {
	file := os.NewFile(uintptr(fd), "managed-file-replacement")
	defer file.Close()
	if err := file.Chmod(original.Mode().Perm()); err != nil {
		return err
	}
	if stat, ok := original.Sys().(*syscall.Stat_t); ok {
		if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(file, content); err != nil {
		return err
	}
	return file.Sync()
}

func verifyManagedReplacement(root, cleanPath, digest string) (bool, error) {
	file, info, err := openManagedNode(root, cleanPath)
	if err != nil || !info.Mode().IsRegular() {
		return true, errors.New("文件原子替换后的身份核验失败")
	}
	defer file.Close()
	content, err := readManagedTextFile(file, info.Size())
	if err != nil || digestText(content) != digest {
		return true, errors.New("文件原子替换后的摘要核验失败")
	}
	return true, nil
}

func readManagedBackup(filePath string, limit int64) (string, error) {
	fd, err := unix.Open(filePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), filePath)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("文件回滚副本不可用")
	}
	return readManagedTextFile(file, limit)
}
