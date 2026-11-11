package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/lifei6671/github-radar/internal/consts"
)

var yamlErrorLine = regexp.MustCompile(`\bline ([0-9]+)\b`)

// Config 包含部署入口和首次导入使用的旧版 YAML 字段。
type Config struct {
	Server          Server          `yaml:"server"`
	Storage         Storage         `yaml:"storage"`
	GitHub          GitHub          `yaml:"github"`
	Scheduler       Scheduler       `yaml:"scheduler"`
	Discovery       Discovery       `yaml:"discovery"`
	CategoriesFile  string          `yaml:"categories_file"`
	Auth            Auth            `yaml:"auth" json:"-"`
	AI              AI              `yaml:"ai"`
	Metrics         Metrics         `yaml:"-"`
	Maintenance     Maintenance     `yaml:"-"`
	Recommendations Recommendations `yaml:"-"`
	path            string
}

// Server 保存监听地址和 HTTP 超时。
type Server struct {
	Listen              string `yaml:"listen" json:"-"`
	SiteURL             string `yaml:"site_url" json:"-"`
	ReadTimeoutSeconds  int    `yaml:"read_timeout_seconds" json:"readTimeoutSeconds"`
	WriteTimeoutSeconds int    `yaml:"write_timeout_seconds" json:"writeTimeoutSeconds"`
}

// Auth 保存启动时读取的管理凭据，不进入运行配置接口或数据库。
type Auth struct {
	AdminPassword string `yaml:"admin_password" json:"-"`
	SessionSecret string `yaml:"session_secret" json:"-"`
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
				Listen  string `yaml:"listen"`
				SiteURL string `yaml:"site_url"`
			} `yaml:"server"`
			Storage struct {
				Path string `yaml:"path"`
			} `yaml:"storage"`
			Auth Auth `yaml:"auth"`
		}
		deployment.Server.Listen, deployment.Storage.Path = cfg.Server.Listen, cfg.Storage.Path
		if err := loadYAML(path, &deployment, false); err != nil {
			return Config{}, err
		}
		cfg.Server.Listen, cfg.Storage.Path = deployment.Server.Listen, deployment.Storage.Path
		cfg.Server.SiteURL, cfg.Auth = deployment.Server.SiteURL, deployment.Auth
	}
	if strings.TrimSpace(cfg.Server.Listen) == "" || strings.TrimSpace(cfg.Storage.Path) == "" {
		return Config{}, errors.New("监听地址和数据库路径不能为空")
	}
	if cfg.Server.SiteURL != "" {
		site, err := url.Parse(cfg.Server.SiteURL)
		if err != nil || (site.Scheme != "http" && site.Scheme != "https") || site.Hostname() == "" ||
			site.User != nil || (site.Path != "" && site.Path != "/") || site.RawQuery != "" || site.ForceQuery ||
			strings.Contains(cfg.Server.SiteURL, "#") {
			return Config{}, errors.New("server.site_url 必须是 HTTP 或 HTTPS 网站源地址，不含路径、查询、片段或账号信息")
		}
		cfg.Server.SiteURL = site.Scheme + "://" + site.Host
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
		// YAML 的类型和别名错误可能带上输入内容，日志只保留定位用的行号。
		if match := yamlErrorLine.FindStringSubmatch(err.Error()); match != nil {
			return fmt.Errorf("解析 YAML 配置: 请检查第 %s 行的语法或字段类型", match[1])
		}
		return errors.New("解析 YAML 配置失败，请对照配置示例检查语法和字段")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("配置文件必须包含单个 YAML 文档")
	}
	return nil
}

// ValidateAdmin 校验 HTTP 服务与备份命令所需的部署配置，离线迁移无需登录配置。
func (cfg Config) ValidateAdmin() error {
	if cfg.Server.SiteURL == "" {
		return errors.New("请在 YAML 配置中设置 server.site_url")
	}
	password := cfg.Auth.AdminPassword
	if utf8.RuneCountInString(strings.TrimSpace(password)) < consts.AdminPasswordMinChars ||
		len(password) > consts.AdminPasswordMaxBytes || strings.ContainsAny(password, "\r\n") {
		return fmt.Errorf("请在 YAML 配置中设置 auth.admin_password，单行至少 %d 个字符且不超过 %d 字节",
			consts.AdminPasswordMinChars, consts.AdminPasswordMaxBytes)
	}
	if len(strings.TrimSpace(cfg.Auth.SessionSecret)) < consts.AdminSessionSecretBytes {
		return fmt.Errorf("请在 YAML 配置中设置 auth.session_secret，至少 %d 字节", consts.AdminSessionSecretBytes)
	}
	return nil
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
