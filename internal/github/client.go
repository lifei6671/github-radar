package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/google/go-github/v88/github"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

// Failure 为队列提供可持久化错误码和上游恢复时刻。
type Failure struct {
	Code   string
	Status int
	Until  int64
}

func (f *Failure) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", f.Code, f.Status)
}

// Client 统一执行认证 REST 请求，使用 go-github 解析资源及分页响应。
type Client struct {
	sdk     *sdk.Client
	token   string
	version string
	limits  limiter
}

func New(db *gorm.DB, cfg config.GitHub, token string) (*Client, error) {
	return newClient(db, cfg, token, nil)
}

func newClient(db *gorm.DB, cfg config.GitHub, token string, base *string) (*Client, error) {
	if token == "" {
		return nil, errors.New("GitHub REST 客户端需要认证令牌")
	}
	c := &Client{token: token, version: cfg.APIVersion, limits: limiter{db, cfg}}
	httpClient := &http.Client{
		Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			origin := via[0].URL
			if len(via) >= 5 || req.URL.Host != origin.Host || req.URL.Scheme != origin.Scheme {
				return http.ErrUseLastResponse
			}
			// 仓库改名后的地址要重新读取元数据，旧 URL 的 ETag 不适用。
			req.Header.Del("If-None-Match")
			return c.limits.reserve(req.Context(), req.Context().Value(policyKey{}).(Policy), time.Now())
		},
	}
	var err error
	c.sdk, err = sdk.NewClient(sdk.WithHTTPClient(httpClient), sdk.WithURLs(base, nil), sdk.WithDisableRateLimitCheck())
	if err != nil {
		return nil, fmt.Errorf("创建 GitHub 客户端: %w", err)
	}
	return c, nil
}

type policyKey struct{}

// RepositoryResult 保留 304 与 200 的区别，只有调用方有可靠基线时才能写 304 快照。
type RepositoryResult struct {
	Repository  *sdk.Repository
	ETag        string
	NotModified bool
}

func (c *Client) Repository(ctx context.Context, owner, name, etag string, p Policy) (RepositoryResult, error) {
	return c.getRepo(ctx, "repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), etag, p)
}

func (c *Client) RepositoryByID(ctx context.Context, id int64, p Policy) (RepositoryResult, error) {
	return c.getRepo(ctx, "repositories/"+strconv.FormatInt(id, 10), "", p)
}

func (c *Client) getRepo(ctx context.Context, path, etag string, p Policy) (RepositoryResult, error) {
	var repo sdk.Repository
	p.Resource = Core
	response, err := c.get(ctx, path, etag, p, &repo)
	if err != nil {
		return RepositoryResult{}, err
	}
	if response.StatusCode == http.StatusNotModified && etag != "" {
		return RepositoryResult{NotModified: true}, nil
	}
	if response.StatusCode != http.StatusOK || repo.GetID() <= 0 || repo.GetFullName() == "" ||
		repo.StargazersCount == nil || repo.ForksCount == nil {
		return RepositoryResult{}, &Failure{Code: "github_invalid_repository", Status: response.StatusCode}
	}
	if repo.GetPrivate() {
		return RepositoryResult{}, &Failure{Code: "github_gone", Status: http.StatusGone}
	}
	return RepositoryResult{Repository: &repo, ETag: response.Header.Get("ETag")}, nil
}

// SearchPage 保留真实分页信息；结果仅供发现使用。
type SearchPage struct {
	Repositories []*sdk.Repository
	Total        int
	NextPage     int
}

func (c *Client) Search(ctx context.Context, query string, page int) (SearchPage, error) {
	values := url.Values{
		"q": {query}, "page": {strconv.Itoa(page)}, "per_page": {"50"},
		"sort": {"stars"}, "order": {"desc"},
	}
	var result sdk.RepositoriesSearchResult
	response, err := c.get(ctx, "search/repositories?"+values.Encode(), "", Policy{Resource: Search}, &result)
	if err != nil {
		return SearchPage{}, err
	}
	if response.StatusCode != http.StatusOK || result.Total == nil || result.GetIncompleteResults() {
		return SearchPage{}, &Failure{Code: "github_incomplete_search", Status: response.StatusCode}
	}
	return SearchPage{result.Repositories, result.GetTotal(), response.NextPage}, nil
}

func (c *Client) get(ctx context.Context, path, etag string, p Policy, target any) (*sdk.Response, error) {
	return c.getMedia(ctx, path, etag, p, target, "")
}

func (c *Client) getMedia(ctx context.Context, path, etag string, p Policy, target any, accept string) (*sdk.Response, error) {
	started := time.Now()
	req, err := c.sdk.NewRequest(context.WithValue(ctx, policyKey{}, p), http.MethodGet, path, nil, sdk.WithVersion(c.version))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if err := c.limits.reserve(ctx, p, started); err != nil {
		return nil, err
	}
	response, requestErr := c.sdk.Do(req, target)
	if errors.Is(requestErr, storage.ErrRepositoryBlocked) {
		return nil, storage.ErrRepositoryBlocked
	}
	var deferred *Failure
	if errors.As(requestErr, &deferred) {
		// 重定向回调也会经过预算闸门，保留它给出的持久化恢复时间。
		return nil, deferred
	}
	if response == nil || response.Response == nil {
		var network net.Error
		switch {
		case errors.Is(requestErr, context.DeadlineExceeded) || (errors.As(requestErr, &network) && network.Timeout()):
			return nil, &Failure{Code: "github_timeout"}
		default:
			return nil, &Failure{Code: "github_network"}
		}
	}

	kind := rateFailure(response.Response, requestErr)
	until, err := c.limits.observe(ctx, p, response.Response, kind, started, time.Now())
	if err != nil {
		return nil, err
	}
	var network net.Error
	if errors.Is(requestErr, context.DeadlineExceeded) || (errors.As(requestErr, &network) && network.Timeout()) {
		return nil, &Failure{Code: "github_timeout"}
	}
	status := response.StatusCode
	if kind != "" {
		return nil, &Failure{Code: "github_" + kind + "_limit", Status: status, Until: until}
	}
	if status == http.StatusNotModified || (status == http.StatusOK && (requestErr == nil || errors.Is(requestErr, errBodyLimit))) {
		return response, nil
	}
	code := "github_invalid_response"
	switch {
	case status == http.StatusUnauthorized:
		code = "github_unauthorized"
	case status == http.StatusForbidden:
		code = "github_forbidden"
	case status == http.StatusNotFound:
		code = "github_not_found"
	case status == http.StatusGone:
		code = "github_gone"
	case status >= 500:
		code = "github_upstream"
	}
	return nil, &Failure{Code: code, Status: status}
}

func rateFailure(response *http.Response, err error) string {
	if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusTooManyRequests {
		return ""
	}
	if response.Header.Get("X-RateLimit-Remaining") == "0" {
		return "primary"
	}
	var abuse *sdk.AbuseRateLimitError
	var apiErr *sdk.ErrorResponse
	if response.StatusCode == http.StatusTooManyRequests || response.Header.Get("Retry-After") != "" ||
		errors.As(err, &abuse) ||
		(errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Message), "secondary rate limit")) {
		return "secondary"
	}
	return ""
}
