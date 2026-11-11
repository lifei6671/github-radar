package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/google/go-github/v88/github"
)

var errBodyLimit = errors.New("GitHub README 响应达到大小限制")

type cappedBody struct {
	bytes.Buffer
	max int
}

func (b *cappedBody) Write(p []byte) (int, error) {
	remaining := b.max - b.Len()
	if len(p) > remaining {
		n, _ := b.Buffer.Write(p[:remaining])
		return n, errBodyLimit
	}
	return b.Buffer.Write(p)
}

// ReadmeResult 的正文只供内存分析，元数据可用于条件检查。
type ReadmeResult struct {
	Body                            []byte
	SHA, ETag, URL                  string
	NotModified, Missing, Truncated bool
}

// Readme 复用 GitHub REST 配额，较大文件按不可变的 blob SHA 读取有限前缀。
func (c *Client) Readme(ctx context.Context, owner, name, etag string, limit int, p Policy) (ReadmeResult, error) {
	path := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	p.Resource = Core
	// GitHub 的内联内容最多约 1 MiB。独立限制 JSON 信封和解码后的正文。
	meta := &cappedBody{max: 2 << 20}
	resp, err := c.getMedia(ctx, path+"/readme", etag, p, meta, "application/vnd.github.object+json")
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) && failure.Status == http.StatusNotFound {
			return ReadmeResult{Missing: true}, nil
		}
		return ReadmeResult{}, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return ReadmeResult{NotModified: true}, nil
	}
	var item sdk.RepositoryContent
	parseErr := json.Unmarshal(meta.Bytes(), &item)
	sha := item.GetSHA()
	if parseErr != nil || (len(sha) != 40 && len(sha) != 64) || item.GetType() != "file" {
		return ReadmeResult{}, &Failure{Code: "github_invalid_readme", Status: resp.StatusCode}
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return ReadmeResult{}, &Failure{Code: "github_invalid_readme", Status: resp.StatusCode}
	}
	result := ReadmeResult{SHA: sha, ETag: resp.Header.Get("ETag")}
	u, parseErr := url.Parse(item.GetHTMLURL())
	if parseErr == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil &&
		strings.HasPrefix(strings.ToLower(u.Path), "/"+strings.ToLower(owner+"/"+name)+"/blob/") {
		u.RawQuery, u.Fragment = "", ""
		result.URL = u.String()
	}
	switch item.GetEncoding() {
	case "base64":
		if item.Content == nil {
			return ReadmeResult{}, &Failure{Code: "github_invalid_readme", Status: resp.StatusCode}
		}
		decoded := base64.NewDecoder(base64.StdEncoding, strings.NewReader(*item.Content))
		result.Body, err = io.ReadAll(io.LimitReader(decoded, int64(limit)+1))
	case "none":
		if item.GetSize() > 0 {
			body := &cappedBody{max: limit + 1}
			_, err = c.getMedia(ctx, path+"/git/blobs/"+sha, "", p, body, "application/vnd.github.raw+json")
			result.Body = body.Bytes()
		}
	default:
		err = errBodyLimit
	}
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return ReadmeResult{}, err
		}
		return ReadmeResult{}, &Failure{Code: "github_invalid_readme", Status: resp.StatusCode}
	}
	result.Truncated = len(result.Body) > limit || item.GetSize() > limit
	if len(result.Body) > limit {
		result.Body = result.Body[:limit]
	}
	return result, nil
}
