package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Config 包含部署入口和首次导入使用的旧版 YAML 字段。
type Config struct {
	Server         Server    `yaml:"server"`
	Storage        Storage   `yaml:"storage"`
	GitHub         GitHub    `yaml:"github"`
	Scheduler      Scheduler `yaml:"scheduler"`
	Discovery      Discovery `yaml:"discovery"`
	CategoriesFile string    `yaml:"categories_file"`
	Auth           struct {
		AdminTokenEnv string `yaml:"admin_token_env"`
	} `yaml:"auth"`
	AI              AI              `yaml:"ai"`
	Metrics         Metrics         `yaml:"-"`
	Maintenance     Maintenance     `yaml:"-"`
	Recommendations Recommendations `yaml:"-"`
	path            string
}

// Server 保存监听地址和 HTTP 超时。
type Server struct {
	Listen              string `yaml:"listen" json:"-"`
	ReadTimeoutSeconds  int    `yaml:"read_timeout_seconds" json:"readTimeoutSeconds"`
	WriteTimeoutSeconds int    `yaml:"write_timeout_seconds" json:"writeTimeoutSeconds"`
}

// Storage 保存数据库路径和 SQLite 连接池配置。
type Storage struct {
	Path          string `yaml:"path" json:"-"`
	WAL           bool   `yaml:"wal" json:"wal"`
	BusyTimeoutMS int    `yaml:"busy_timeout_ms" json:"busyTimeoutMs"`
	MaxOpenConns  int    `yaml:"max_open_conns" json:"maxOpenConns"`
}

// AI 保存可编辑的供应商设置。旧环境变量名只在首次导入时读取。
type AI struct {
	Enabled    bool    `yaml:"enabled" json:"enabled"`
	BaseURL    string  `yaml:"base_url" json:"baseUrl"`
	Model      string  `yaml:"model" json:"model"`
	APIKeyEnv  string  `yaml:"api_key_env" json:"apiKeyEnv"`
	BaseURLEnv string  `yaml:"base_url_env" json:"-"`
	ModelEnv   string  `yaml:"model_env" json:"-"`
	Summary    Summary `yaml:"-" json:"summary"`
}

// Load 只解析打开数据库前需要的信息，旧业务字段留到首次导入时读取。
func Load(path string) (Config, error) {
	cfg := Config{
		Server: Server{
			Listen: "127.0.0.1:8080", ReadTimeoutSeconds: 10, WriteTimeoutSeconds: 30,
		},
		Storage: Storage{
			Path: "./data/github-radar.db", WAL: true, BusyTimeoutMS: 5000, MaxOpenConns: 4,
		},
	}
	cfg.Auth.AdminTokenEnv = "ADMIN_TOKEN"
	cfg.GitHub, cfg.Scheduler, cfg.Discovery = collectionDefaults()
	cfg.Metrics, cfg.Maintenance = DefaultMetrics(), DefaultMaintenance()
	cfg.AI.Summary, cfg.Recommendations = DefaultSummary(), DefaultRecommendations()
	cfg.AI.Enabled = true
	cfg.AI.BaseURLEnv = "AI_BASE_URL"
	cfg.AI.APIKeyEnv = "AI_API_KEY"
	cfg.AI.ModelEnv = "AI_MODEL"
	cfg.path = path

	if path != "" {
		var deployment struct {
			Server struct {
				Listen string `yaml:"listen"`
			} `yaml:"server"`
			Storage struct {
				Path string `yaml:"path"`
			} `yaml:"storage"`
			Auth struct {
				AdminTokenEnv string `yaml:"admin_token_env"`
			} `yaml:"auth"`
		}
		deployment.Server.Listen, deployment.Storage.Path = cfg.Server.Listen, cfg.Storage.Path
		deployment.Auth.AdminTokenEnv = cfg.Auth.AdminTokenEnv
		if err := loadYAML(path, &deployment, false); err != nil {
			return Config{}, err
		}
		cfg.Server.Listen, cfg.Storage.Path = deployment.Server.Listen, deployment.Storage.Path
		cfg.Auth.AdminTokenEnv = deployment.Auth.AdminTokenEnv
	}
	if strings.TrimSpace(cfg.Server.Listen) == "" || strings.TrimSpace(cfg.Storage.Path) == "" ||
		strings.TrimSpace(cfg.Auth.AdminTokenEnv) == "" {
		return Config{}, errors.New("监听地址、数据库路径和管理员令牌环境变量名不能为空")
	}
	return cfg, nil
}

// SeedConfig 在首次导入时解析兼容的旧版业务字段。
func (cfg Config) SeedConfig() (Config, error) {
	if cfg.path != "" {
		if err := loadYAML(cfg.path, &cfg, true); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func loadYAML(path string, dest any, strict bool) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开配置文件: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(strict)
	if err := decoder.Decode(dest); err != nil {
		return fmt.Errorf("解析 YAML 配置: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("配置文件必须包含单个 YAML 文档")
	}
	return nil
}

// AdminToken 验证服务的必需凭据，空值和带空白的令牌无法用于 Bearer 鉴权。
func (cfg Config) AdminToken() (string, error) {
	token := os.Getenv(cfg.Auth.AdminTokenEnv)
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("缺少管理员令牌，请设置环境变量 %s", cfg.Auth.AdminTokenEnv)
	}
	if strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("管理员令牌环境变量 %s 的值不能含空白", cfg.Auth.AdminTokenEnv)
	}
	return token, nil
}

// MissingAISettings 返回启用 AI 时缺少的设置或密钥环境变量名。
func (cfg Config) MissingAISettings() []string {
	if !cfg.AI.Enabled {
		return nil
	}
	var missing []string
	if strings.TrimSpace(cfg.AI.BaseURL) == "" {
		missing = append(missing, "ai.baseUrl")
	}
	if strings.TrimSpace(cfg.AI.Model) == "" {
		missing = append(missing, "ai.model")
	}
	if strings.TrimSpace(os.Getenv(cfg.AI.APIKeyEnv)) == "" {
		missing = append(missing, cfg.AI.APIKeyEnv)
	}
	return missing
}
