package configs

import _ "embed"

// DefaultCategories 随可执行文件分发，仅在首次写入数据库配置时导入。
//
//go:embed categories.yaml
var DefaultCategories []byte
