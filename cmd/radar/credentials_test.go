package main

import (
	"bytes"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lifei6671/github-radar/internal/auth"
	"github.com/lifei6671/github-radar/internal/consts"
)

func TestCredentialCommands(t *testing.T) {
	t.Run("hash-password", func(t *testing.T) {
		const password = "测试密码-with trailing space "
		path := filepath.Join(t.TempDir(), "stdin")
		if err := os.WriteFile(path, []byte(password+"\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdin, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer stdin.Close()

		var out bytes.Buffer
		if err := runCredentialCommand(consts.CommandHashPassword, nil, stdin, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		encoded := strings.TrimSuffix(out.String(), "\n")
		hash, err := auth.ParsePasswordHash(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !hash.Verify(password) || hash.Verify(strings.TrimSpace(password)) {
			t.Fatal("哈希必须匹配输入的原密码，包括末尾空格")
		}
		if _, err := auth.ParsePasswordHash(encoded[:len(encoded)-1] + "\n"); err == nil {
			t.Fatal("哈希被换行截断时必须拒绝")
		}
	})

	t.Run("generate-session-secret", func(t *testing.T) {
		generate := func() string {
			var out bytes.Buffer
			if err := runCredentialCommand(consts.CommandGenerateSessionSecret, nil, nil, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			return strings.TrimSuffix(out.String(), "\n")
		}
		first := generate()
		key, err := hex.DecodeString(first)
		if err != nil || len(key) != consts.AdminSessionSecretBytes || first == generate() {
			t.Fatal("每次生成应返回独立的 32 字节随机密钥")
		}
	})
}
