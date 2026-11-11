package backup

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const objectSuffix = ".db.gz"

type Object struct {
	ObjectKey     string     `json:"objectKey"`
	Date          string     `json:"date"`
	Size          int64      `json:"size"`
	RawSize       int64      `json:"rawSize"`
	SchemaVersion int64      `json:"schemaVersion"`
	GeneratedAt   *time.Time `json:"generatedAt"`
	SHA256        string     `json:"sha256,omitempty"`
}

type ObjectPage struct {
	Items      []Object `json:"items"`
	NextCursor *string  `json:"nextCursor"`
}

type objectInfo struct {
	size     int64
	metadata map[string]string
}

type objectStore interface {
	keys(context.Context, string) ([]string, error)
	head(context.Context, string) (objectInfo, error)
	get(context.Context, string) (io.ReadCloser, error)
	put(context.Context, string, io.ReadSeeker, int64, string, map[string]string) error
	remove(context.Context, string) error
}

type factory func(Connection) objectStore

type s3Store struct {
	client *s3.Client
	bucket string
}

// newR2 复用 AWS S3 SDK 签名和重试。写入只提交一次，响应不明由 HEAD 核对。
func newR2(c Connection) objectStore {
	httpClient := &http.Client{
		Timeout:       90 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(c.Endpoint), Region: "auto", UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""),
		HTTPClient:  httpClient, RetryMaxAttempts: 2,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &s3Store{client: client, bucket: c.Bucket}
}

func (s *s3Store) keys(ctx context.Context, prefix string) ([]string, error) {
	pager := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), Delimiter: aws.String("/"), MaxKeys: aws.Int32(1000),
	}, func(p *s3.ListObjectsV2PaginatorOptions) { p.StopOnDuplicateToken = true })
	keys := make([]string, 0)
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, ErrR2
		}
		for _, item := range page.Contents {
			keys = append(keys, aws.ToString(item.Key))
		}
	}
	return keys, nil
}

func (s *s3Store) head(ctx context.Context, key string) (objectInfo, error) {
	response, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return objectInfo{}, ErrInvalidObject
		}
		return objectInfo{}, ErrR2
	}
	return objectInfo{aws.ToInt64(response.ContentLength), response.Metadata}, nil
}

func (s *s3Store) get(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, ErrR2
	}
	return response.Body, nil
}

func (s *s3Store) put(ctx context.Context, key string, body io.ReadSeeker, size int64, md5 string, metadata map[string]string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size),
		ContentType: aws.String("application/gzip"), ContentMD5: aws.String(md5), Metadata: metadata,
	}, func(o *s3.Options) { o.Retryer = aws.NopRetryer{} })
	if err != nil {
		var apiErr smithy.APIError
		var responseErr *smithyhttp.ResponseError
		if errors.As(err, &responseErr) {
			status := responseErr.HTTPStatusCode()
			if status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
				return ErrR2
			}
			return ErrUploadUnknown
		}
		if errors.As(err, &apiErr) && apiErr.ErrorFault() == smithy.FaultClient {
			return ErrR2
		}
		return ErrUploadUnknown
	}
	return nil
}

func (s *s3Store) remove(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return ErrR2
	}
	return nil
}

func testConnection(ctx context.Context, store objectStore, c Connection) error {
	if _, err := store.keys(ctx, c.Prefix); err != nil {
		return err
	}
	key := c.Prefix + ".radar-check-" + rand.Text()
	const contents = "github-radar"
	checksum := md5.Sum([]byte(contents))
	putErr := store.put(ctx, key, strings.NewReader(contents), int64(len(contents)), base64.StdEncoding.EncodeToString(checksum[:]), nil)
	var headErr error
	if putErr == nil {
		info, err := store.head(ctx, key)
		headErr = err
		if err == nil && info.size != int64(len(contents)) {
			headErr = ErrR2
		}
	}
	// 即使测试请求断开，也尽量删除本次明确命名的探针对象。
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	if err := store.remove(cleanup, key); err != nil || putErr != nil || headErr != nil {
		return ErrR2
	}
	return nil
}

func objectDate(prefix, key string) (string, bool) {
	file, ok := strings.CutPrefix(key, prefix)
	if !ok || len(file) != len(time.DateOnly)+len(objectSuffix) || !strings.HasSuffix(file, objectSuffix) {
		return "", false
	}
	date := strings.TrimSuffix(file, objectSuffix)
	parsed, err := time.Parse(time.DateOnly, date)
	return date, err == nil && parsed.Format(time.DateOnly) == date
}

func objectFromInfo(key, date string, info objectInfo) Object {
	raw, _ := strconv.ParseInt(info.metadata["raw-size"], 10, 64)
	schema, _ := strconv.ParseInt(info.metadata["schema-version"], 10, 64)
	object := Object{ObjectKey: key, Date: date, Size: info.size, RawSize: raw, SchemaVersion: schema, SHA256: info.metadata["sha256"]}
	if generated, err := time.Parse(time.RFC3339Nano, info.metadata["generated-at"]); err == nil {
		generated = generated.UTC()
		object.GeneratedAt = &generated
	}
	return object
}

func listObjects(ctx context.Context, store objectStore, c Connection, limit int, cursor string) (ObjectPage, error) {
	page := ObjectPage{Items: make([]Object, 0, limit)}
	if limit < 1 || limit > 100 {
		return page, ErrInvalidObject
	}
	if cursor != "" {
		if _, ok := objectDate(c.Prefix, c.Prefix+cursor+objectSuffix); !ok {
			return page, ErrInvalidObject
		}
	}
	keys, err := store.keys(ctx, c.Prefix)
	if err != nil {
		return page, err
	}
	slices.Sort(keys)
	slices.Reverse(keys)
	for _, key := range keys {
		date, ok := objectDate(c.Prefix, key)
		if !ok || (cursor != "" && date >= cursor) {
			continue
		}
		if len(page.Items) == limit {
			last := page.Items[len(page.Items)-1].Date
			page.NextCursor = &last
			break
		}
		info, err := store.head(ctx, key)
		if errors.Is(err, ErrInvalidObject) {
			continue
		}
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, objectFromInfo(key, date, info))
	}
	return page, nil
}

func cleanObjects(ctx context.Context, store objectStore, s Settings, now time.Time) error {
	local := now.In(s.location())
	cutoff := local.AddDate(0, 0, 1-s.RetentionDays).Format(time.DateOnly)
	keys, err := store.keys(ctx, s.R2.Prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if date, ok := objectDate(s.R2.Prefix, key); ok && date < cutoff {
			if err := store.remove(ctx, key); err != nil {
				return err
			}
		}
	}
	return nil
}
