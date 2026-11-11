-- +goose Up
-- 专题与顶部分类独立维护，初始条目只随本次迁移写入。
CREATE TABLE project_themes (
    slug TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    topics_json TEXT NOT NULL CHECK (json_valid(topics_json) AND json_type(topics_json) = 'array'),
    position INTEGER NOT NULL
) WITHOUT ROWID;

INSERT INTO project_themes (slug, name, description, topics_json, position) VALUES
    ('terminal-tools', '终端开发工具', '在终端运行的命令行工具、开发助手和工作流工具。', '["cli","command-line","terminal-tool"]', 0),
    ('browser-automation', '浏览器自动化', '用于网页操作、浏览器测试和自动化任务的开源项目。', '["browser-automation","browser-agent","web-automation"]', 1),
    ('mcp-servers', 'MCP 服务端', '为模型和智能体提供工具或数据的 MCP 服务端实现。', '["mcp-server","mcp-servers"]', 2),
    ('coding-agents', '代码智能体', '辅助阅读、编写、修改和验证代码的开源智能体。', '["coding-agent","ai-coding-agent","code-agent"]', 3),
    ('llm-tooling', '大模型工具链', '用于大语言模型推理、评测和应用开发的工具。', '["llm","llm-inference","llm-evaluation"]', 4),
    ('webhook-tools', 'Webhook 与事件集成', '接收、转发或处理 Webhook 和应用事件的开源工具。', '["webhook","webhooks","event-routing"]', 5);

-- +goose Down
DROP TABLE project_themes;
