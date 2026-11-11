-- +goose Up
-- AI 推断主题与 GitHub 原生 Topics 分开保存，仓库删除时一并清理。
CREATE TABLE repo_ai_topics (
    repo_id INTEGER PRIMARY KEY REFERENCES repositories(repo_id) ON DELETE CASCADE,
    topics_json TEXT NOT NULL CHECK(json_valid(topics_json) AND json_type(topics_json) = 'array'),
    source_fingerprint TEXT NOT NULL,
    model_provider TEXT NOT NULL,
    model_name TEXT NOT NULL,
    prompt_version INTEGER NOT NULL,
    generated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE repo_ai_topics;
