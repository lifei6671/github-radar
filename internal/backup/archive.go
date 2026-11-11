package backup

import (
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type checkedReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r checkedReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func ensureSpace(path string, parts ...int64) error {
	free, err := availableBytes(path)
	if err != nil {
		return ErrSpace
	}
	for _, size := range parts {
		if size < 0 || uint64(size) > free {
			return ErrSpace
		}
		free -= uint64(size)
	}
	return nil
}

func stageSize(path string) int64 {
	entries, _ := os.ReadDir(path)
	var size int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if info, err := entry.Info(); err == nil {
			size += info.Size()
		}
	}
	return size
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

// compressFile 关闭 Gzip 后重新完整解码，验证压缩流和未压缩文件大小。
func compressFile(ctx context.Context, source, dest string) (raw, size int64, digest, contentMD5 string, err error) {
	in, err := os.Open(source)
	if err != nil {
		return 0, 0, "", "", ErrDatabase
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, "", "", ErrSpace
	}
	sha, md := sha256.New(), md5.New()
	gzipWriter := gzip.NewWriter(io.MultiWriter(out, sha, md))
	raw, err = io.Copy(gzipWriter, checkedReader{ctx, in})
	err = errors.Join(err, gzipWriter.Close(), out.Close())
	if err != nil {
		return 0, 0, "", "", Fault("backup_compression_failed")
	}
	archive, err := os.Open(dest)
	if err != nil {
		return 0, 0, "", "", ErrDatabase
	}
	defer archive.Close()
	file, err := archive.Stat()
	if err != nil {
		return 0, 0, "", "", ErrDatabase
	}
	check, err := gzip.NewReader(archive)
	if err != nil {
		return 0, 0, "", "", Fault("backup_compression_failed")
	}
	n, readErr := io.Copy(io.Discard, checkedReader{ctx, check})
	if closeErr := check.Close(); readErr != nil || closeErr != nil || n != raw {
		return 0, 0, "", "", Fault("backup_compression_failed")
	}
	return raw, file.Size(), hex.EncodeToString(sha.Sum(nil)), base64.StdEncoding.EncodeToString(md.Sum(nil)), nil
}

func upload(ctx context.Context, store objectStore, key, date, path string, obj Object, md5 string) (Object, error) {
	body, err := os.Open(path)
	if err != nil {
		return Object{}, ErrDatabase
	}
	defer body.Close()
	metadata := map[string]string{
		"sha256": obj.SHA256, "raw-size": strconv.FormatInt(obj.RawSize, 10),
		"schema-version": strconv.FormatInt(obj.SchemaVersion, 10), "generated-at": obj.GeneratedAt.Format(time.RFC3339Nano),
	}
	putErr := store.put(ctx, key, body, obj.Size, md5, metadata)
	if errors.Is(putErr, ErrR2) {
		return Object{}, putErr
	}
	// PUT 超时也可能已经提交，给 HEAD 一个独立的有限窗口确认。
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	remote, err := store.head(checkCtx, key)
	if err != nil || remote.size != obj.Size {
		return Object{}, ErrUploadUnknown
	}
	for name, expected := range metadata {
		if remote.metadata[name] != expected {
			return Object{}, ErrUploadUnknown
		}
	}
	return objectFromInfo(key, date, remote), nil
}

func download(ctx context.Context, store objectStore, c Connection, key, dir string) (string, Object, error) {
	date, ok := objectDate(c.Prefix, key)
	if !ok {
		return "", Object{}, ErrInvalidObject
	}
	info, err := store.head(ctx, key)
	if err != nil {
		return "", Object{}, err
	}
	obj := objectFromInfo(key, date, info)
	sum, err := hex.DecodeString(obj.SHA256)
	if err != nil || len(sum) != sha256.Size || obj.RawSize <= 0 || obj.Size <= 0 ||
		obj.RawSize == math.MaxInt64 || obj.Size == math.MaxInt64 || obj.SchemaVersion < 1 || obj.GeneratedAt == nil {
		return "", Object{}, ErrInvalidObject
	}
	if err := ensureSpace(dir, obj.Size, obj.RawSize, obj.RawSize); err != nil {
		return "", Object{}, err
	}

	remote, err := store.get(ctx, key)
	if err != nil {
		return "", Object{}, err
	}
	defer remote.Close()
	packed := filepath.Join(dir, "source.db.gz")
	dst, err := os.OpenFile(packed, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", Object{}, ErrSpace
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(checkedReader{ctx, remote}, obj.Size+1))
	if closeErr := dst.Close(); err != nil || closeErr != nil || n != obj.Size || hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(sum) {
		return "", Object{}, Fault("backup_checksum_mismatch")
	}
	source, err := os.Open(packed)
	if err != nil {
		return "", Object{}, ErrDatabase
	}
	defer source.Close()
	unzip, err := gzip.NewReader(source)
	if err != nil {
		return "", Object{}, Fault("backup_gzip_invalid")
	}
	defer unzip.Close()
	path := filepath.Join(dir, "source.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", Object{}, ErrSpace
	}
	raw, err := io.Copy(file, io.LimitReader(checkedReader{ctx, unzip}, obj.RawSize+1))
	if closeErr := file.Close(); err != nil || closeErr != nil || raw != obj.RawSize {
		return "", Object{}, Fault("backup_gzip_invalid")
	}
	return path, obj, nil
}
