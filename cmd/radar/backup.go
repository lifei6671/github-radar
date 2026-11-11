package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/lifei6671/github-radar/internal/backup"
	"github.com/lifei6671/github-radar/internal/consts"
)

// runBackup 使用运行中服务的同一协调器，避免离线命令与恢复并发写库。
func runBackup(ctx context.Context, listen, token string, logger *slog.Logger) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	default:
		addr := net.ParseIP(host)
		if host != "localhost" && (addr == nil || !addr.IsLoopback()) {
			return errors.New("backup 命令需要本机监听地址，请使用管理页面")
		}
	}
	base := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
	ctx, cancel := context.WithTimeout(ctx, 11*time.Minute)
	defer cancel()
	client := http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path string, dest any) error {
		base.Path = path
		req, err := http.NewRequestWithContext(ctx, method, base.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return json.NewDecoder(res.Body).Decode(dest)
		}
		var data struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(res.Body).Decode(&data)
		return fmt.Errorf("备份接口 HTTP %d %s", res.StatusCode, data.Code)
	}
	var accepted struct {
		OperationID string `json:"operationId"`
	}
	if err := request(http.MethodPost, "/api/v1/system/backup/run", &accepted); err != nil {
		return err
	}
	logger.Info("备份已接受", "operationId", accepted.OperationID)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var op backup.Operation
		if err := request(http.MethodGet, "/api/v1/system/backup/operations/"+accepted.OperationID, &op); err != nil {
			return err
		}
		switch op.Status {
		case consts.StatusFailed:
			return fmt.Errorf("备份失败 %s", op.ErrorCode)
		case consts.StatusCompleted:
			logger.Info("备份完成", "operationId", op.ID, "warning", op.Warning)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
