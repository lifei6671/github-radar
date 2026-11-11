package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/lifei6671/github-radar/internal/backup"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

type liveR2Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

func liveR2Connection(t *testing.T) backup.Connection {
	t.Helper()
	var connection backup.Connection
	if path := os.Getenv("RADAR_ACCEPTANCE_R2_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("无法读取本地 R2 验收凭据文件")
		}
		var file struct {
			Endpoint, Bucket, Prefix string
			Primary                  liveR2Credentials
		}
		if json.Unmarshal(raw, &file) != nil {
			t.Fatal("R2 验收凭据文件不是有效 JSON")
		}
		connection = backup.Connection{Endpoint: file.Endpoint, Bucket: file.Bucket, Prefix: file.Prefix,
			AccessKeyID: file.Primary.AccessKeyID, SecretAccessKey: file.Primary.SecretAccessKey}
	} else {
		connection = backup.Connection{Endpoint: os.Getenv("RADAR_ACCEPTANCE_R2_ENDPOINT"), Bucket: os.Getenv("RADAR_ACCEPTANCE_R2_BUCKET"),
			Prefix: os.Getenv("RADAR_ACCEPTANCE_R2_PREFIX"), AccessKeyID: os.Getenv("RADAR_ACCEPTANCE_R2_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("RADAR_ACCEPTANCE_R2_SECRET_ACCESS_KEY")}
		if connection.Endpoint == "" {
			t.Skip("真实 R2 验收需要显式设置 RADAR_ACCEPTANCE_R2_FILE 或测试桶环境变量")
		}
		if connection.Prefix == "" {
			connection.Prefix = "github-radar/acceptance/"
		}
	}
	if strings.TrimSpace(connection.Prefix) == "" {
		t.Fatal("R2 验收凭据文件须提供测试前缀")
	}
	connection, err := backup.NormalizeConnection(connection)
	if err != nil {
		t.Fatal("R2 测试桶参数不完整或格式无效")
	}
	// 只在本轮唯一子前缀内写入和清理测试对象。
	connection.Prefix += "radar-acceptance-" + strings.ToLower(rand.Text()) + "/"
	return connection
}

type liveR2Fixture struct {
	client *s3.Client
	bucket string
}

func (r liveR2Fixture) keys(ctx context.Context, prefix string) ([]string, error) {
	pages := s3.NewListObjectsV2Paginator(r.client, &s3.ListObjectsV2Input{Bucket: aws.String(r.bucket), Prefix: aws.String(prefix)})
	var keys []string
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (r liveR2Fixture) put(t *testing.T, key string, body []byte, metadata map[string]string) {
	t.Helper()
	digest := md5.Sum(body)
	_, err := r.client.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key),
		Body: bytes.NewReader(body), ContentMD5: aws.String(base64.StdEncoding.EncodeToString(digest[:])), Metadata: metadata})
	if err != nil {
		t.Fatal("真实 R2 写入测试对象失败")
	}
}

func (r liveR2Fixture) read(t *testing.T, key string) ([]byte, map[string]string) {
	t.Helper()
	result, err := r.client.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatal("真实 R2 下载测试对象失败")
	}
	defer result.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Body, 16<<20))
	if err != nil || int64(len(data)) != aws.ToInt64(result.ContentLength) {
		t.Fatal("真实 R2 对象长度不符")
	}
	return data, result.Metadata
}

func (r liveR2Fixture) verifyKeys(t *testing.T, prefix string, want []string) {
	t.Helper()
	actual, err := r.keys(t.Context(), prefix)
	if err != nil {
		t.Fatal("真实 R2 列表请求失败")
	}
	sort.Strings(want)
	if strings.Join(actual, "\n") != strings.Join(want, "\n") {
		t.Fatalf("真实 R2 对象集合不符，实际 %d 个，预期 %d 个", len(actual), len(want))
	}
}

func acceptanceBackup(t *testing.T, s acceptanceServer) backup.Operation {
	t.Helper()
	const runPath = "/api/v1/system/backup/run"
	accepted := acceptanceJSON[struct{ StatusURL string }](t, s, http.MethodPost, runPath, nil, 202)
	op := waitAcceptanceJSON[backup.Operation](t, s, accepted.StatusURL, func(op backup.Operation) bool { return op.Status != "running" })
	if op.Status != "completed" {
		t.Fatalf("真实 R2 备份失败: %s", op.ErrorCode)
	}
	return op
}

const acceptanceRepoID = 771245
const acceptanceSummary = "验收项目帮助团队组织本地开发任务，支持按组安排脚本和查看执行结果。"

type acceptanceRepoView struct {
	ID            int64
	FullName      string
	Stars         int
	Followed      bool
	LastSuccessAt *string
	SummaryZh     *string
	SummaryStatus string
	Growth24h     struct {
		Delta   *int64
		Quality string
	}
}

func seededR2Database(t *testing.T, path string) {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err = prepareDatabase(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := storage.Open(t.Context(), cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(5 * time.Minute)
	desc := "Workflow fixture for end-to-end acceptance."
	rows := []any{
		&storage.Repository{RepoID: acceptanceRepoID, Owner: "fixture", Name: "radar-workflow", FullName: "fixture/radar-workflow", NameIsCurrent: true,
			HTMLURL: "https://github.com/fixture/radar-workflow", Description: &desc, TopicsJSON: `["mcp"]`, Stars: 1250, Forks: 5,
			Grade: "A", State: "active", LastSuccessAt: new(now.UnixMilli()), DiscoveredAt: now.AddDate(0, 0, -20).UnixMilli(), UpdatedAt: now.UnixMilli()},
		&storage.RepoSnapshot{RepoID: acceptanceRepoID, SampleSlot: now.AddDate(0, 0, -1).UnixMilli(), ObservedAt: now.AddDate(0, 0, -1).UnixMilli(), Stars: 1000, Forks: 5},
		&storage.RepoSnapshot{RepoID: acceptanceRepoID, SampleSlot: now.UnixMilli(), ObservedAt: now.UnixMilli(), Stars: 1250, Forks: 5},
		&storage.AISummary{RepoID: acceptanceRepoID, SourceFingerprint: "readme:fixture", SourceQuality: "sufficient", SourceLanguage: "en",
			SummaryZh: acceptanceSummary, FeaturesJSON: `["脚本分组"]`, AudienceJSON: `["开发者"]`, ValueTagsJSON: `["自动化"]`,
			ModelProvider: "fixture", ModelName: "fixture-model", PromptVersion: 1, SummaryStatus: "ready", GeneratedAt: now.UnixMilli()},
		&storage.TopicCandidate{RepoID: acceptanceRepoID, AngleKey: "acceptance", TitleZh: "本地开发工作流", ReaderValue: "组织开发任务",
			SourceURL: "https://github.com/fixture/radar-workflow", Reason: "验收用选题", TopicScore: 80, Status: "accepted", CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()},
		&storage.Job{Type: "refresh_repo", RepoID: new(int64(acceptanceRepoID)), DedupKey: "acceptance:pending", PayloadJSON: "{}", Status: "pending",
			AvailableAt: now.Add(time.Hour).UnixMilli(), MaxAttempts: 3, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("无法写入验收数据 %T: %v", row, err)
		}
	}
}

func verifyR2Restored(t *testing.T, s acceptanceServer, conn backup.Connection) backup.Operation {
	t.Helper()
	state := acceptanceJSON[backup.Status](t, s, http.MethodGet, "/api/v1/system/backup/status", nil, 200)
	if !state.Initialized || !state.AutoAIResumeRequired || state.LastRestore == nil ||
		!state.LastRestore.DatabaseApplied || state.LastRestore.Status != "completed" {
		t.Fatal("恢复状态与 AI 暂停标记不符")
	}
	view := acceptanceJSON[acceptanceRepoView](t, s, http.MethodGet, "/api/v1/repos/"+strconv.Itoa(acceptanceRepoID), nil, 200)
	if view.Stars != 1250 || view.SummaryZh == nil || *view.SummaryZh != acceptanceSummary || view.Followed ||
		view.Growth24h.Delta == nil || *view.Growth24h.Delta != 250 {
		t.Fatal("恢复后的仓库、增长和摘要不符")
	}
	topics := acceptanceJSON[struct {
		Items []struct{ Status, TitleZh string }
	}](t, s, http.MethodGet, "/api/v1/topics?status=accepted", nil, 200)
	if len(topics.Items) != 1 || topics.Items[0].TitleZh != "本地开发工作流" {
		t.Fatal("已审核选题未恢复")
	}
	settings := acceptanceJSON[backup.SettingsView](t, s, http.MethodGet, "/api/v1/system/backup/settings", nil, 200)
	if !settings.R2.Configured || settings.R2.Prefix != conn.Prefix {
		t.Fatal("恢复后的 R2 连接未使用本次配置")
	}
	status, raw, err := s.request(t.Context(), http.MethodGet, "/api/v1/system/backup/settings", nil)
	if err != nil || status != 200 || bytes.Contains(raw, []byte(conn.SecretAccessKey)) || bytes.Contains(raw, []byte(conn.AccessKeyID)) {
		t.Fatal("设置响应泄露了完整 R2 凭据")
	}
	return *state.LastRestore
}

func openLiveR2(t *testing.T) (backup.Connection, liveR2Fixture) {
	t.Helper()
	root := liveR2Connection(t)
	client := s3.New(s3.Options{BaseEndpoint: aws.String(root.Endpoint), Region: "auto", UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(root.AccessKeyID, root.SecretAccessKey, ""),
		HTTPClient:  &http.Client{Timeout: 15 * time.Second}, RetryMaxAttempts: 2,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	r2 := liveR2Fixture{client, root.Bucket}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		keys, err := r2.keys(ctx, root.Prefix)
		if err != nil {
			t.Error("未能列出本轮 R2 测试前缀以完成清理")
			return
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, root.Prefix) {
				t.Error("R2 清理范围异常")
				continue
			}
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(root.Bucket), Key: aws.String(key)}); err != nil {
				t.Error("未能删除本轮 R2 测试对象")
			}
		}
		remaining, err := r2.keys(ctx, root.Prefix)
		if err != nil || len(remaining) != 0 {
			t.Error("R2 测试前缀清理未确认")
		} else {
			t.Log("本轮 R2 测试对象已全部清理")
		}
	})
	return root, r2
}

// 只改测试副本中的凭据，模拟备份后原密钥被轮换的安装恢复。
func archiveWithOldR2Credentials(t *testing.T, archive []byte, metadata map[string]string) ([]byte, map[string]string) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal("无法解压真实备份的测试副本")
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, 16<<20))
	if closeErr := reader.Close(); readErr != nil || closeErr != nil {
		t.Fatal("读取真实备份的测试副本失败")
	}
	path := filepath.Join(t.TempDir(), "legacy.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := storage.OpenBootstrap(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := pool.ExecContext(t.Context(), `UPDATE system_settings SET value_json=json_set(value_json,
		'$.r2.access_key_id','retired-acceptance-id','$.r2.secret_access_key','retired-acceptance-secret')
		WHERE key='backup.settings.v1'`)
	if closeErr := pool.Close(); writeErr != nil || closeErr != nil {
		t.Fatal("写入轮换前的测试凭据失败")
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, writeErr = writer.Write(raw)
	if closeErr := writer.Close(); writeErr != nil || closeErr != nil {
		t.Fatal("压缩轮换测试副本失败")
	}
	body := compressed.Bytes()
	digest := sha256.Sum256(body)
	result := maps.Clone(metadata)
	result["sha256"] = hex.EncodeToString(digest[:])
	result["raw-size"] = strconv.Itoa(len(raw))
	return body, result
}

func TestAcceptanceLiveR2(t *testing.T) {
	root, r2 := openLiveR2(t)
	t.Setenv("AI_BASE_URL", "")
	t.Setenv("AI_MODEL", "")
	sourceConn, targetConn := root, root
	sourceConn.Prefix += "source/"
	targetConn.Prefix += "restore/"
	sourceConfig := acceptanceConfig(t, t.TempDir())
	seededR2Database(t, sourceConfig)
	source := startAcceptanceServer(t, sourceConfig)
	acceptanceJSON[map[string]any](t, source, http.MethodPost, "/api/v1/setup/initialize", nil, 200)

	var location *time.Location
	for _, zone := range []string{"Asia/Shanghai", "UTC", "America/New_York"} {
		loc, err := time.LoadLocation(zone)
		if err == nil && time.Now().In(loc).Hour() >= 6 && time.Now().In(loc).Hour() < 21 {
			location = loc
			break
		}
	}
	if location == nil {
		t.Fatal("未能选择距离日期边界足够远的验收时区")
	}
	now := time.Now().In(location)
	dayKey := func(prefix string, days int) string {
		return prefix + now.AddDate(0, 0, -days).Format(time.DateOnly) + ".db.gz"
	}
	settingsPath := "/api/v1/system/backup/settings"
	schedule := backup.Schedule{Hour: 0, Minute: 0, Timezone: location.String()}
	settings := backup.SettingsUpdate{Schedule: schedule, RetentionDays: 5, R2: &sourceConn}
	acceptanceJSON[backup.SettingsView](t, source, http.MethodPut, settingsPath, settings, 200)
	acceptanceBackup(t, source)
	original, metadata := r2.read(t, dayKey(sourceConn.Prefix, 0))
	sum := sha256.Sum256(original)
	if metadata["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatal("真实 R2 压缩对象的 SHA-256 不匹配")
	}
	keep := []string{dayKey(sourceConn.Prefix, 0)}
	for d := 1; d <= 5; d++ {
		key := dayKey(sourceConn.Prefix, d)
		r2.put(t, key, original, metadata)
		if d < 5 {
			keep = append(keep, key)
		}
	}
	nested := sourceConn.Prefix + "nested/2000-01-01.db.gz"
	invalid := sourceConn.Prefix + "2026-02-30.db.gz"
	sibling := root.Prefix + "other/2000-01-01.db.gz"
	for _, key := range []string{nested, invalid, sibling} {
		r2.put(t, key, []byte("acceptance boundary"), nil)
	}
	keep = append(keep, nested, invalid, sibling)
	acceptanceBackup(t, source)
	archive, metadata2 := r2.read(t, dayKey(sourceConn.Prefix, 0))
	if metadata2["generated-at"] == metadata["generated-at"] {
		t.Fatal("同一天的对象未被覆盖")
	}
	r2.verifyKeys(t, root.Prefix, keep)
	badConnection := sourceConn
	badConnection.SecretAccessKey += "invalid"
	denied := settings
	denied.R2 = &badConnection
	rejected := acceptanceJSON[map[string]string](t, source, http.MethodPut, settingsPath, denied, 502)
	_, unchanged := r2.read(t, dayKey(sourceConn.Prefix, 0))
	if rejected["code"] != "r2_request_failed" || unchanged["sha256"] != metadata2["sha256"] {
		t.Fatal("R2 凭据校验失败改变了已有备份")
	}
	t.Log("真实 R2 同日覆盖及 6 个日期的 5 日保留通过，嵌套、无效日期和邻接前缀对象保留")

	// 先补回一个过期对象，当天已经备份的情况下只能由独立清理删除。
	r2.put(t, dayKey(sourceConn.Prefix, 5), original, metadata)
	settings.Enabled, settings.R2 = true, nil
	acceptanceJSON[backup.SettingsView](t, source, http.MethodPut, settingsPath, settings, 200)
	cleaned := waitAcceptanceJSON[backup.Status](t, source, "/api/v1/system/backup/status", func(status backup.Status) bool {
		return status.LastCleanup != nil && status.LastCleanup.Status != "running"
	})
	if cleaned.LastCleanup.Status != "completed" {
		t.Fatal("独立定时保留清理失败")
	}
	r2.verifyKeys(t, root.Prefix, keep)
	auto := root
	auto.Prefix += "daily/"
	settings.R2 = &auto
	acceptanceJSON[backup.SettingsView](t, source, http.MethodPut, settingsPath, settings, 200)
	daily := waitAcceptanceJSON[backup.Status](t, source, "/api/v1/system/backup/status", func(status backup.Status) bool {
		return status.LastBackup != nil && status.LastBackup.ObjectKey == dayKey(auto.Prefix, 0)
	})
	if daily.LastBackup.Size <= 0 {
		t.Fatal("热更新后未自动生成每日备份")
	}
	source.stop()
	source = startAcceptanceServer(t, sourceConfig)
	persisted := acceptanceJSON[backup.SettingsView](t, source, http.MethodGet, settingsPath, nil, 200)
	if !persisted.Enabled || persisted.RetentionDays != 5 || persisted.Schedule != schedule || persisted.R2.Prefix != auto.Prefix {
		t.Fatal("重启后 R2 设置或计划未保持")
	}
	source.stop()
	t.Log("真实 R2 热更新、独立保留任务、自动补跑和配置重启持久化通过")

	// 从另一 R2 前缀恢复，确保旧库配置由本次已校验连接整体覆盖。
	archive, metadata2 = archiveWithOldR2Credentials(t, archive, metadata2)
	r2.put(t, dayKey(targetConn.Prefix, 0), archive, metadata2)
	targetConfig := acceptanceConfig(t, t.TempDir())
	target := startAcceptanceServer(t, targetConfig)
	pid := target.pid
	acceptanceBrowser(t, target, "云端首次安装",
		"RADAR_ACCEPTANCE_R2_ENDPOINT="+targetConn.Endpoint, "RADAR_ACCEPTANCE_R2_BUCKET="+targetConn.Bucket,
		"RADAR_ACCEPTANCE_R2_PREFIX="+targetConn.Prefix, "RADAR_ACCEPTANCE_R2_ACCESS_KEY_ID="+targetConn.AccessKeyID,
		"RADAR_ACCEPTANCE_R2_SECRET_ACCESS_KEY="+targetConn.SecretAccessKey,
		"RADAR_ACCEPTANCE_BACKUP_DATE="+now.Format(time.DateOnly))
	first := verifyR2Restored(t, target, targetConn)
	if !target.running() || acceptanceJSON[map[string]any](t, target, http.MethodPost, "/api/v1/setup/initialize", nil, 409)["code"] != "already_initialized" {
		t.Fatal("首次恢复后进程或安装入口状态异常")
	}
	repoPath := "/api/v1/repos/" + strconv.Itoa(acceptanceRepoID)
	acceptanceJSON[map[string]any](t, target, http.MethodPost, repoPath+"/follow", map[string]bool{"follow": true}, 200)
	if changed := acceptanceJSON[acceptanceRepoView](t, target, http.MethodGet, repoPath, nil, 200); !changed.Followed {
		t.Fatal("恢复前业务变更未生效")
	}
	acceptanceBrowser(t, target, "云端后台恢复", "RADAR_ACCEPTANCE_BACKUP_DATE="+now.Format(time.DateOnly))
	restored := verifyR2Restored(t, target, targetConn)
	if restored.ID == first.ID || !target.running() {
		t.Fatal("后台恢复应在原进程执行新操作")
	}
	t.Logf("真实 R2 安装和后台一键恢复在同一 PID %d 内通过，维护 %d / %d 毫秒，暂存 %d / %d 字节", pid, first.MaintenanceMS, restored.MaintenanceMS, first.StagingBytes, restored.StagingBytes)

	badKey := dayKey(targetConn.Prefix, 1)
	archive[0] ^= 0xff
	r2.put(t, badKey, archive, metadata2)
	corrupt := acceptanceJSON[struct{ StatusURL string }](t, target, http.MethodPost, "/api/v1/system/backup/restore", map[string]string{"objectKey": badKey}, 202)
	failure := waitAcceptanceJSON[backup.Operation](t, target, corrupt.StatusURL, func(op backup.Operation) bool { return op.Status != "running" })
	if failure.Status != "failed" || failure.ErrorCode != "backup_checksum_mismatch" || failure.DatabaseApplied {
		t.Fatal("真实 R2 损坏对象未在恢复前被拒绝")
	}
	if view := acceptanceJSON[acceptanceRepoView](t, target, http.MethodGet, repoPath, nil, 200); view.Stars != 1250 {
		t.Fatal("损坏备份影响了原库")
	}
	target.stop()
	target = startAcceptanceServer(t, targetConfig)
	status := acceptanceJSON[backup.Status](t, target, http.MethodGet, "/api/v1/system/backup/status", nil, 200)
	if status.LastRestore == nil || status.LastRestore.ID != restored.ID || !status.AutoAIResumeRequired {
		t.Fatal("重启未保留恢复凭证和自动 AI 暂停")
	}
	acceptanceJSON[map[string]bool](t, target, http.MethodPost, "/api/v1/system/ai/resume", map[string]bool{"acceptIncompleteUsage": true}, 200)
	acceptanceBackup(t, target)
	_, newMetadata := r2.read(t, dayKey(targetConn.Prefix, 0))
	if newMetadata["generated-at"] == metadata2["generated-at"] {
		t.Fatal("恢复后备份未使用新 R2 前缀")
	}
	target.stop()

	pool, err := storage.OpenBootstrap(t.Context(), filepath.Join(filepath.Dir(targetConfig), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var value string
	if err := pool.QueryRowContext(t.Context(), "SELECT value_json FROM system_settings WHERE key='backup.settings.v1'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	var saved struct {
		R2 struct {
			Prefix string
			liveR2Credentials
		}
	}
	if json.Unmarshal([]byte(value), &saved) != nil || saved.R2.Prefix != targetConn.Prefix ||
		saved.R2.AccessKeyID != targetConn.AccessKeyID || saved.R2.SecretAccessKey != targetConn.SecretAccessKey {
		t.Fatal("恢复库未保存本次完整 R2 连接")
	}
	var jobState string
	if err := pool.QueryRowContext(t.Context(), "SELECT status FROM jobs WHERE dedup_key='acceptance:pending'").Scan(&jobState); err != nil || jobState != "canceled" {
		t.Fatal("恢复库的旧未完成任务未取消")
	}
	t.Log("真实 R2 损坏对象拒绝、恢复凭证与 AI 暂停跨重启、恢复后新前缀备份通过")
}
