package trending

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	sdk "github.com/google/go-github/v88/github"
)

// Entry 区分页面显示的窗口 Star 增量与累计 Star。
type Entry struct {
	Repository    *sdk.Repository
	Rank          int
	ReportedStars *int
}

type Client struct {
	HTTP    *http.Client
	BaseURL string
}

func New(timeout time.Duration) *Client {
	return &Client{HTTP: &http.Client{Timeout: timeout}, BaseURL: "https://github.com/"}
}

// Fetch 使用独立的未认证 HTTP 客户端，API 令牌不会发往 HTML 页面。
func (c *Client) Fetch(ctx context.Context, language, period string) ([]Entry, error) {
	target, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	// 语言名传入原始路径值，由 URL 编码 C# 的井号。
	target.Path = "/trending/" + language
	target.RawQuery = url.Values{"since": {period}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "github-radar")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("Trending 网络请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Trending HTTP %d", response.StatusCode)
	}
	const maxPageBytes = 4 << 20
	// HTML parser 会容忍截断，必须在解析前区分大小上限与真正的 EOF。
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes+1))
	if err != nil {
		return nil, errors.New("读取 Trending 页面失败")
	}
	if len(data) > maxPageBytes {
		return nil, errors.New("Trending 页面超过大小上限，保留旧榜单")
	}
	return Parse(bytes.NewReader(data))
}

var fullNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Parse 只接受完整仓库榜单。登录页、结构变化和部分损坏都会返回错误。
func Parse(reader io.Reader) ([]Entry, error) {
	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return nil, fmt.Errorf("解析 Trending HTML: %w", err)
	}
	if !strings.Contains(doc.Find("h1").Text(), "Trending") {
		return nil, errors.New("Trending 页面缺少榜单标题")
	}
	rows := doc.Find("article.Box-row")
	if rows.Length() == 0 {
		blank := doc.Find(".blankslate-heading").Text()
		if strings.Contains(blank, "any trending repositories") {
			return []Entry{}, nil
		}
		return nil, errors.New("Trending 页面结构变化或榜单缺失")
	}
	if rows.Length() > 100 {
		return nil, errors.New("Trending 榜单数量异常")
	}
	var entries []Entry
	names := make(map[string]bool)
	rows.EachWithBreak(func(index int, row *goquery.Selection) bool {
		href, _ := row.Find("h2 a").First().Attr("href")
		fullName := strings.Trim(href, "/")
		if !fullNamePattern.MatchString(fullName) || names[strings.ToLower(fullName)] {
			err = errors.New("Trending 榜单包含无效或重复仓库")
			return false
		}
		names[strings.ToLower(fullName)] = true
		parts := strings.Split(fullName, "/")
		description := strings.TrimSpace(row.Find("p").First().Text())
		language := strings.TrimSpace(row.Find("[itemprop=programmingLanguage]").Text())
		repo := &sdk.Repository{
			Owner: &sdk.User{Login: &parts[0]}, Name: &parts[1], FullName: &fullName,
			HTMLURL: sdk.Ptr("https://github.com/" + fullName), Description: &description,
		}
		if language != "" {
			repo.Language = &language
		}
		row.Find("[data-hydro-click]").Each(func(_ int, link *goquery.Selection) {
			var event struct {
				Payload struct {
					RepositoryID int64 `json:"repository_id"`
				} `json:"payload"`
			}
			value, _ := link.Attr("data-hydro-click")
			if json.Unmarshal([]byte(value), &event) == nil && event.Payload.RepositoryID > 0 {
				repo.ID = &event.Payload.RepositoryID
			}
		})
		if stars, parseErr := number(row.Find("a[href$='/stargazers']").First().Text()); parseErr == nil {
			repo.StargazersCount = &stars
		}
		if forks, parseErr := number(row.Find("a[href$='/forks']").First().Text()); parseErr == nil {
			repo.ForksCount = &forks
		}
		entry := Entry{Repository: repo, Rank: index + 1}
		row.Find("span").Each(func(_ int, span *goquery.Selection) {
			text := strings.TrimSpace(span.Text())
			fields := strings.Fields(text)
			if len(fields) > 1 && strings.Contains(text, "stars ") {
				if count, parseErr := number(fields[0]); parseErr == nil {
					entry.ReportedStars = &count
				}
			}
		})
		entries = append(entries, entry)
		return true
	})
	return entries, err
}

func number(text string) (int, error) {
	return strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(text), ",", ""))
}
