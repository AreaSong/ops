package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (engine *Engine) ManagedFile(
	ctx context.Context,
	actor, rootID, relativePath string,
) (model.ManagedFileView, error) {
	if err := engine.authorizeManagedFileRead(ctx, actor, rootID); err != nil {
		return model.ManagedFileView{}, err
	}
	root, _, cleanPath, err := engine.resolveManagedPath(rootID, relativePath)
	if err != nil {
		return model.ManagedFileView{}, err
	}
	node, info, err := openManagedNode(root, cleanPath)
	if err != nil {
		return model.ManagedFileView{}, err
	}
	defer node.Close()
	view := model.ManagedFileView{
		RootID: rootID, Path: cleanPath, Size: info.Size(),
		ReadOnly: engine.catalog.Files.ReadOnly, IsDirectory: info.IsDir(),
	}
	if info.IsDir() {
		view.Entries, err = listManagedDirectory(cleanPath, node)
		return view, err
	}
	if !info.Mode().IsRegular() {
		return model.ManagedFileView{}, errors.New("受管路径不是普通文件或目录")
	}
	view.Content, err = readManagedTextFile(node, engine.catalog.Files.MaxFileBytes)
	if err != nil {
		return model.ManagedFileView{}, err
	}
	view.Digest = digestText(view.Content)
	return view, nil
}

func (engine *Engine) authorizeManagedFileRead(ctx context.Context, actor, rootID string) error {
	err := engine.authorize(ctx, actor, model.PermissionRead, "file:"+rootID)
	if err == nil {
		return nil
	}
	if platformErr := engine.authorizePlatform(ctx, actor, model.PermissionRead, "files"); platformErr == nil {
		return nil
	}
	return err
}

func (engine *Engine) ProposeManagedFile(
	ctx context.Context,
	actor string,
	request model.ManagedFileRequest,
) (model.ManagedFileProposal, bool, error) {
	if err := engine.validateManagedFileProposal(ctx, actor, request); err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	current, err := engine.ManagedFile(ctx, actor, request.RootID, request.Path)
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if current.IsDirectory || request.ExpectedDigest != current.Digest {
		return model.ManagedFileProposal{}, false, errors.New("文件基线摘要已变化，请重新读取")
	}
	id, err := newUUID()
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	proposal := model.ManagedFileProposal{
		ID: id, IdempotencyKey: request.IdempotencyKey, ActorHash: actor,
		RootID: request.RootID, Path: current.Path, ExpectedDigest: current.Digest,
		ProposedDigest: digestText(request.Content), Content: request.Content,
		State: "proposed", CreatedAt: time.Now().UTC(),
	}
	proposal.ConfirmationPhrase = fmt.Sprintf("批准文件变更 %s/%s %s",
		proposal.RootID, proposal.Path, shortDigest(proposal.ProposedDigest))
	requestDigest := digestText(strings.Join([]string{
		actor, proposal.RootID, proposal.Path, proposal.ExpectedDigest, proposal.ProposedDigest,
	}, "\x00"))
	saved, created, err := engine.store.SaveManagedFileProposal(ctx, proposal, requestDigest)
	return saved, created, err
}

func (engine *Engine) validateManagedFileProposal(
	ctx context.Context,
	actor string,
	request model.ManagedFileRequest,
) error {
	policy := engine.catalog.Files
	if policy == nil || !policy.Enabled {
		return errors.New("文件管理尚未启用")
	}
	if policy.ReadOnly {
		return errors.New("文件管理策略仅允许读取")
	}
	if request.Mode != "propose" {
		return errors.New("文件变更只支持 propose 模式")
	}
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return errors.New("文件提案幂等键无效")
	}
	if len(request.Content) == 0 || int64(len(request.Content)) > policy.MaxFileBytes ||
		!utf8.ValidString(request.Content) || strings.ContainsRune(request.Content, '\x00') {
		return errors.New("文件提案内容为空、过大或不是有效文本")
	}
	return engine.authorize(ctx, actor, model.PermissionManageConfig, "file:"+request.RootID)
}

func (engine *Engine) resolveManagedPath(
	rootID, relativePath string,
) (string, string, string, error) {
	policy := engine.catalog.Files
	if policy == nil || !policy.Enabled {
		return "", "", "", errors.New("文件管理尚未启用")
	}
	root, ok := policy.Roots[rootID]
	if !ok {
		return "", "", "", errors.New("文件根目录不在白名单")
	}
	cleanPath, err := cleanRelativePath(relativePath)
	if err != nil {
		return "", "", "", err
	}
	root = filepath.Clean(root)
	target := filepath.Join(root, filepath.FromSlash(cleanPath))
	if err := rejectManagedSymlinks(root, target); err != nil {
		return "", "", "", err
	}
	if err := rejectManagedInsecurePath(root, target); err != nil {
		return "", "", "", err
	}
	return root, target, cleanPath, nil
}

func cleanRelativePath(value string) (string, error) {
	if len(value) > 4096 || strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
		return "", errors.New("受管文件路径无效")
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", errors.New("受管文件路径越过白名单根目录")
	}
	return filepath.ToSlash(cleaned), nil
}

func rejectManagedSymlinks(root, target string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("文件白名单根路径必须是非符号链接目录")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("受管文件路径越过白名单根目录")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("受管文件路径包含符号链接")
		}
	}
	return nil
}

func rejectManagedInsecurePath(root, target string) error {
	// Development runners may use user-owned temporary roots. Production
	// runners run as root, so enforce ownership at the point of use while
	// applying the mode restriction to every path component in every environment.
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("受管文件路径越过白名单根目录")
	}
	paths := []string{root}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	for index, path := range paths {
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) && index == len(paths)-1 {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("受管文件路径包含符号链接")
		}
		if index < len(paths)-1 && !info.IsDir() {
			return errors.New("受管文件中间路径必须是目录")
		}
		if info.Mode().Perm()&0o022 != 0 {
			return errors.New("受管文件路径组件不能由 group/other 写入")
		}
		if os.Geteuid() == 0 {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 {
				return errors.New("受管文件路径组件必须由 root 拥有")
			}
		}
	}
	return nil
}

func listManagedDirectory(relativeDirectory string, directory *os.File) ([]model.ManagedFileEntry, error) {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	result := make([]model.ManagedFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		result = append(result, model.ManagedFileEntry{
			Name: entry.Name(), Path: path.Join(relativeDirectory, entry.Name()), Size: info.Size(),
			IsDirectory: info.IsDir(), ModifiedAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].IsDirectory != result[right].IsDirectory {
			return result[left].IsDirectory
		}
		return result[left].Name < result[right].Name
	})
	return result, nil
}

func readManagedTextFile(file *os.File, limit int64) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		return "", errors.New("受管文件超过大小限制")
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
		return "", errors.New("受管文件不是可显示的 UTF-8 文本")
	}
	return string(data), nil
}
