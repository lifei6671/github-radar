package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/lifei6671/github-radar/internal/consts"
)

const (
	passwordMemory  = 19 * 1024
	passwordTime    = 2
	passwordThreads = 1
	saltBytes       = 16
	hashBytes       = 32
)

var hashPrefix = fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, passwordMemory, passwordTime, passwordThreads)
var errPasswordHash = errors.New("无效的后台密码哈希，请用 hash-password 命令生成")

type PasswordHash struct {
	salt [saltBytes]byte
	key  [hashBytes]byte
}

func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(strings.TrimSpace(password)) < consts.AdminPasswordMinChars ||
		len(password) > consts.AdminPasswordMaxBytes || strings.ContainsAny(password, "\r\n") || !utf8.ValidString(password) {
		return "", fmt.Errorf("后台密码须为单行，至少 %d 个字符且不超过 %d 字节", consts.AdminPasswordMinChars, consts.AdminPasswordMaxBytes)
	}
	var salt [saltBytes]byte
	rand.Read(salt[:])
	key := argon2.IDKey([]byte(password), salt[:], passwordTime, passwordMemory, passwordThreads, hashBytes)
	return hashPrefix + base64.RawStdEncoding.EncodeToString(salt[:]) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func ParsePasswordHash(encoded string) (PasswordHash, error) {
	var hash PasswordHash
	rest, ok := strings.CutPrefix(encoded, hashPrefix)
	salt, key, separated := strings.Cut(rest, "$")
	encoding := base64.RawStdEncoding.Strict()
	if !ok || !separated || len(salt) != encoding.EncodedLen(saltBytes) || len(key) != encoding.EncodedLen(hashBytes) {
		return hash, errPasswordHash
	}
	saltLen, saltErr := encoding.Decode(hash.salt[:], []byte(salt))
	keyLen, keyErr := encoding.Decode(hash.key[:], []byte(key))
	if saltErr != nil || keyErr != nil || saltLen != saltBytes || keyLen != hashBytes {
		return PasswordHash{}, errPasswordHash
	}
	return hash, nil
}

func (hash PasswordHash) Verify(password string) bool {
	if len(password) > consts.AdminPasswordMaxBytes {
		return false
	}
	key := argon2.IDKey([]byte(password), hash.salt[:], passwordTime, passwordMemory, passwordThreads, hashBytes)
	return subtle.ConstantTimeCompare(key, hash.key[:]) == 1
}

func GenerateSessionSecret() string {
	var key [consts.AdminSessionSecretBytes]byte
	rand.Read(key[:])
	return hex.EncodeToString(key[:])
}
