// Package snapshot implements file history snapshots backed by git object storage.
// 对标 Claude Code 的 File History Snapshots（/rewind 的基础）。
//
// 设计:
//   - 每次 Edit/Write 执行前，将被修改文件的原始内容存为 git blob object
//   - 快照元数据存在内存中（进程内持久化），同时写入 .edoc/snapshots/ 目录作为持久化
//   - /rewind 命令恢复文件到指定快照前的状态
//   - 不污染 git stash、不创建 commit、不影响工作区 index
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Snapshot 记录一次文件修改前的状态。
type Snapshot struct {
	ID        string    `json:"id"`
	FilePath  string    `json:"file_path"`
	BlobHash  string    `json:"blob_hash"`  // git blob object hash（空=文件不存在）
	CreatedAt time.Time `json:"created_at"`
	ToolName  string    `json:"tool_name"`  // "Edit" or "Write"
}

// Store 管理文件快照。
type Store struct {
	mu      sync.RWMutex
	entries []*Snapshot
	workDir string
	seq     int
}

// NewStore 创建快照存储，workDir 是 git 仓库根目录。
func NewStore(workDir string) *Store {
	s := &Store{workDir: workDir}
	s.load()
	return s
}

// snapshotDir 返回快照元数据目录。
func (s *Store) snapshotDir() string {
	return filepath.Join(s.workDir, ".edoc", "snapshots")
}

// Save 在文件被修改前保存其当前内容为快照。
// 如果文件不存在（Write 新建），blobHash 为空字符串。
// 返回快照 ID。
func (s *Store) Save(filePath, toolName string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	id := fmt.Sprintf("%d", s.seq)

	snap := &Snapshot{
		ID:        id,
		FilePath:  filePath,
		CreatedAt: time.Now(),
		ToolName:  toolName,
	}

	// 尝试将文件内容存为 git blob object
	if _, err := os.Stat(filePath); err == nil {
		hash, err := gitHashObject(s.workDir, filePath)
		if err == nil {
			snap.BlobHash = hash
		}
	}
	// 文件不存在时 BlobHash 为空（表示"此文件之前不存在"）

	s.entries = append(s.entries, snap)
	s.persist(snap)
	return id, nil
}

// List 返回所有快照，按时间倒序。
func (s *Store) List() []*Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Snapshot, len(s.entries))
	copy(result, s.entries)
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

// Get 按 ID 获取快照。
func (s *Store) Get(id string) (*Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return nil, false
}

// Restore 将文件恢复到快照时的状态。
// 如果 BlobHash 为空，表示文件在快照时不存在，恢复 = 删除文件。
func (s *Store) Restore(id string) error {
	snap, ok := s.Get(id)
	if !ok {
		return fmt.Errorf("snapshot not found: %s", id)
	}

	if snap.BlobHash == "" {
		// 文件在快照时不存在 → 删除
		if err := os.Remove(snap.FilePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", snap.FilePath, err)
		}
		return nil
	}

	// 从 git blob object 恢复内容
	content, err := gitCatFile(s.workDir, snap.BlobHash)
	if err != nil {
		return fmt.Errorf("read blob %s: %w", snap.BlobHash, err)
	}

	if err := os.MkdirAll(filepath.Dir(snap.FilePath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(snap.FilePath, content, 0644); err != nil {
		return fmt.Errorf("write %s: %w", snap.FilePath, err)
	}
	return nil
}

// RewindN 恢复最近 n 个快照（按时间倒序逐个恢复）。
// 返回实际恢复的快照列表。
func (s *Store) RewindN(n int) ([]*Snapshot, []error) {
	all := s.List()
	if n > len(all) {
		n = len(all)
	}
	targets := all[:n]

	var restored []*Snapshot
	var errs []error
	for _, snap := range targets {
		if err := s.Restore(snap.ID); err != nil {
			errs = append(errs, fmt.Errorf("snapshot %s (%s): %w", snap.ID, snap.FilePath, err))
		} else {
			restored = append(restored, snap)
		}
	}
	return restored, errs
}

// persist 将单个快照写入 .edoc/snapshots/<id>.json。
func (s *Store) persist(snap *Snapshot) {
	dir := s.snapshotDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, snap.ID+".json"), data, 0644)
}

// load 从 .edoc/snapshots/ 加载已有快照（进程重启后恢复）。
func (s *Store) load() {
	dir := s.snapshotDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var snaps []*Snapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var snap Snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			continue
		}
		snaps = append(snaps, &snap)
		// 更新 seq
		var id int
		fmt.Sscanf(snap.ID, "%d", &id)
		if id > s.seq {
			s.seq = id
		}
	}
	// 按时间排序
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})
	s.entries = snaps
}

// ── git helpers ──────────────────────────────────────────────────────────────

// gitHashObject stores a file as a git blob object and returns its hash.
// 等价于: git hash-object -w <file>
func gitHashObject(workDir, filePath string) (string, error) {
	cmd := exec.Command("git", "hash-object", "-w", filePath)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitCatFile reads a git blob object by hash.
// 等价于: git cat-file blob <hash>
func gitCatFile(workDir, hash string) ([]byte, error) {
	cmd := exec.Command("git", "cat-file", "blob", hash)
	cmd.Dir = workDir
	return cmd.Output()
}
