package storage

const RepoEmbeddingTable = "repo_embeddings"

// RepoEmbedding 保存模型空间、来源指纹和向量。README 仅在采集与分析期间驻留内存。
type RepoEmbedding struct {
	RepoID            int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	ModelKey          string `gorm:"column:model_key"`
	SourceFingerprint string `gorm:"column:source_fingerprint"`
	Vector            []byte `gorm:"column:vector"`
	NextCheckAt       int64  `gorm:"column:next_check_at"`
}

func (RepoEmbedding) TableName() string { return RepoEmbeddingTable }

var RepoEmbeddingColumns = struct {
	RepoID            string
	ModelKey          string
	SourceFingerprint string
	Vector            string
	NextCheckAt       string
}{
	RepoID:            "repo_id",
	ModelKey:          "model_key",
	SourceFingerprint: "source_fingerprint",
	Vector:            "vector",
	NextCheckAt:       "next_check_at",
}
