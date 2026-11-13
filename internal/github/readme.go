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
	"path"
	"strings"

	sdk "github.com/google/go-github/v88/github"

	"github.com/lifei6671/github-radar/internal/consts"
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

// ReadmeResult 包含限长的 README 正文，元数据可用于条件检查和后台续译。
type ReadmeResult struct {
	Body                            []byte
	SHA, ETag, URL, Path            string
	NotModified, Missing, Truncated bool
}

// Readme 复用 GitHub REST 配额，较大文件按不可变的 blob SHA 读取有限前缀。
func (c *Client) Readme(ctx context.Context, owner, name, etag string, limit int, p Policy) (ReadmeResult, error) {
	return c.readme(ctx, owner, name, "/readme", etag, limit, p)
}

func (c *Client) LocalizedReadme(ctx context.Context, owner, name, filename string, limit int, p Policy) (ReadmeResult, error) {
	file := (&url.URL{Path: filename}).EscapedPath()
	return c.readme(ctx, owner, name, "/contents/"+file, "", limit, p)
}

// ReadmeSiblings lists paths beside the default README so unlinked translations can be found.
func (c *Client) ReadmeSiblings(ctx context.Context, owner, name, directory string, p Policy) ([]string, error) {
	entries, err := c.readmeDirectory(ctx, owner, name, directory, p)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.GetType() == "file" {
			paths = append(paths, entry.GetPath())
		}
	}
	return paths, nil
}

// ReadmeSHA 从所在目录的元数据核对指定文件，无需重新下载 README。
func (c *Client) ReadmeSHA(ctx context.Context, owner, name, filename string, p Policy) (string, error) {
	if filename == "" {
		return "", &Failure{Code: consts.CodeGitHubInvalidReadme}
	}
	entries, err := c.readmeDirectory(ctx, owner, name, path.Dir(filename), p)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.GetPath() != filename {
			continue
		}
		if entry.GetType() == "symlink" {
			return c.linkedReadmeSHA(ctx, owner, name, filename, entry.GetSHA(), p)
		}
		if entry.GetType() == "file" {
			return entry.GetSHA(), nil
		}
		return "", nil
	}
	if len(entries) >= 1000 {
		// Contents 最多列出一千项；仅在目标未列出时逐层查 Git 树。
		repo, err := c.RepositoryByID(ctx, p.RepositoryID, p)
		if err != nil {
			return "", err
		}
		return c.readmeTreeSHA(ctx, owner, name, repo.Repository.GetDefaultBranch(), filename, p)
	}
	return "", nil
}

// 链接 blob 只含目标路径，读取路径后再查目标文件的元数据。
func (c *Client) linkedReadmeSHA(ctx context.Context, owner, name, filename, linkSHA string, p Policy) (string, error) {
	endpoint := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/git/blobs/" + url.PathEscape(linkSHA)
	p.Resource = Core
	link := &cappedBody{max: 4097}
	_, err := c.getMedia(ctx, endpoint, "", p, link, "application/vnd.github.raw+json")
	if err != nil {
		return "", err
	}
	target := path.Join(path.Dir(filename), link.String())
	if link.Len() == 0 || link.Len() > 4096 || strings.HasPrefix(link.String(), "/") || target == ".." || strings.HasPrefix(target, "../") {
		return "", &Failure{Code: consts.CodeGitHubInvalidReadme}
	}
	entries, err := c.readmeDirectory(ctx, owner, name, path.Dir(target), p)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.GetPath() == target && entry.GetType() == "file" {
			return entry.GetSHA(), nil
		}
	}
	return "", &Failure{Code: consts.CodeGitHubInvalidReadme}
}

func (c *Client) readmeTreeSHA(ctx context.Context, owner, name, branch, filename string, p Policy) (string, error) {
	endpoint := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/git/trees/"
	p.Resource = Core
	ref := branch
	segments := strings.Split(filename, "/")
	for i, segment := range segments {
		var tree sdk.Tree
		if _, err := c.get(ctx, endpoint+url.PathEscape(ref), "", p, &tree); err != nil {
			return "", err
		}
		ref = ""
		for _, entry := range tree.Entries {
			if entry.GetPath() == segment {
				if i == len(segments)-1 && entry.GetMode() == "120000" {
					return c.linkedReadmeSHA(ctx, owner, name, filename, entry.GetSHA(), p)
				}
				ref = entry.GetSHA()
				break
			}
		}
		if ref == "" {
			if tree.GetTruncated() {
				return "", &Failure{Code: consts.CodeGitHubInvalidReadme}
			}
			return "", nil
		}
	}
	return ref, nil
}

func (c *Client) readmeDirectory(ctx context.Context, owner, name, directory string, p Policy) ([]sdk.RepositoryContent, error) {
	endpoint := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/contents"
	if directory != "." {
		endpoint += "/" + (&url.URL{Path: directory}).EscapedPath()
	}
	p.Resource = Core
	var entries []sdk.RepositoryContent
	_, err := c.getMedia(ctx, endpoint, "", p, &entries, "application/vnd.github+json")
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) && failure.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

func (c *Client) readme(ctx context.Context, owner, name, endpoint, etag string, limit int, p Policy) (ReadmeResult, error) {
	path := "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	p.Resource = Core
	// GitHub 的内联内容最多约 1 MiB。独立限制 JSON 信封和解码后的正文。
	meta := &cappedBody{max: 2 << 20}
	resp, err := c.getMedia(ctx, path+endpoint, etag, p, meta, "application/vnd.github.object+json")
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
		return ReadmeResult{}, &Failure{Code: consts.CodeGitHubInvalidReadme, Status: resp.StatusCode}
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return ReadmeResult{}, &Failure{Code: consts.CodeGitHubInvalidReadme, Status: resp.StatusCode}
	}
	result := ReadmeResult{SHA: sha, ETag: resp.Header.Get("ETag"), Path: item.GetPath()}
	u, parseErr := url.Parse(item.GetHTMLURL())
	if parseErr == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil &&
		strings.HasPrefix(strings.ToLower(u.Path), "/"+strings.ToLower(owner+"/"+name)+"/blob/") {
		u.RawQuery, u.Fragment = "", ""
		result.URL = u.String()
	}
	switch item.GetEncoding() {
	case "base64":
		if item.Content == nil {
			return ReadmeResult{}, &Failure{Code: consts.CodeGitHubInvalidReadme, Status: resp.StatusCode}
		}
		decoded := base64.NewDecoder(base64.StdEncoding, strings.NewReader(*item.Content))
		result.Body, err = io.ReadAll(io.LimitReader(decoded, int64(limit)+1))
	case "none":
		if item.GetSize() > 0 {
			body := &cappedBody{max: limit + 1}
			_, err = c.getMedia(ctx, path+"/git/blobs/"+sha, "", p, body, "application/vnd.github.raw+json")
			if err != nil {
				return ReadmeResult{}, err
			}
			result.Body = body.Bytes()
		}
	default:
		err = errBodyLimit
	}
	if err != nil {
		return ReadmeResult{}, &Failure{Code: consts.CodeGitHubInvalidReadme, Status: resp.StatusCode}
	}
	result.Truncated = len(result.Body) > limit || item.GetSize() > limit
	if len(result.Body) > limit {
		result.Body = result.Body[:limit]
	}
	return result, nil
}
