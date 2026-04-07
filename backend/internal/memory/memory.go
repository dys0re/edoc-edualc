package memory

import "time"

// MemoryType 记忆类型，对标 memdir/memoryTypes.ts
type MemoryType string

const (
	TypeUser      MemoryType = "user"
	TypeFeedback  MemoryType = "feedback"
	TypeProject   MemoryType = "project"
	TypeReference MemoryType = "reference"
)

// Header 记忆文件的元信息（从 frontmatter 解析）
// 对标 memdir/memoryScan.ts:MemoryHeader
type Header struct {
	Filename    string
	FilePath    string
	Name        string
	Description string
	Type        MemoryType
	ModTime     time.Time
}

// ParseMemoryType 解析类型字符串，无效值返回空字符串
func ParseMemoryType(s string) MemoryType {
	switch MemoryType(s) {
	case TypeUser, TypeFeedback, TypeProject, TypeReference:
		return MemoryType(s)
	}
	return ""
}
