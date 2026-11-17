// Package secure provides encryption, password hashing and output redaction.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/crypto/argon2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func Random(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic("system random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func ID() string             { return Random(12) }
func Digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

type Vault struct{ aead cipher.AEAD }

func NewVault(key string) (*Vault, error) {
	b, e := base64.StdEncoding.DecodeString(key)
	if e != nil || len(b) != 32 {
		return nil, errors.New("MUSE_MASTER_KEY must be a base64 encoded 32-byte key")
	}
	block, e := aes.NewCipher(b)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	return &Vault{a}, e
}
func (v *Vault) Seal(context string, b []byte) ([]byte, error) {
	n := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return nil, e
	}
	return v.aead.Seal(n, n, b, []byte(context)), nil
}
func (v *Vault) Open(context string, b []byte) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("invalid encrypted record")
	}
	out, e := v.aead.Open(nil, b[:n], b[n:], []byte(context))
	if e != nil {
		return nil, errors.New("unable to decrypt record; verify master key")
	}
	return out, nil
}
func PasswordHash(p string) string {
	salt := make([]byte, 16)
	_, e := rand.Read(salt)
	if e != nil {
		panic("system random source unavailable")
	}
	h := argon2.IDKey([]byte(p), salt, 2, 32*1024, 1, 32)
	return base64.RawStdEncoding.EncodeToString(salt) + "." + base64.RawStdEncoding.EncodeToString(h)
}
func VerifyPassword(p, encoded string) bool {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[0])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[1])
	if e != nil || len(want) != 32 {
		return false
	}
	h := argon2.IDKey([]byte(p), salt, 2, 32*1024, 1, 32)
	return subtle.ConstantTimeCompare(h, want) == 1
}
func ValidPassword(p string) bool { return len(p) >= 12 && len(p) <= 256 }
func BackupEncrypt(pass string, b []byte) (string, error) {
	if !ValidPassword(pass) {
		return "", errors.New("backup passphrase must have 12–256 bytes")
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	k := argon2.IDKey([]byte(pass), salt, 2, 32*1024, 1, 32)
	v, e := NewVault(base64.StdEncoding.EncodeToString(k))
	if e != nil {
		return "", e
	}
	out, e := v.Seal("muse-bridge-backup-v1", b)
	return "MB1." + base64.StdEncoding.EncodeToString(append(salt, out...)), e
}
func BackupDecrypt(pass, blob string) ([]byte, error) {
	if !strings.HasPrefix(blob, "MB1.") {
		return nil, errors.New("unsupported backup format")
	}
	b, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(blob, "MB1."))
	if e != nil || len(b) < 44 {
		return nil, errors.New("invalid backup")
	}
	k := argon2.IDKey([]byte(pass), b[:16], 2, 32*1024, 1, 32)
	v, e := NewVault(base64.StdEncoding.EncodeToString(k))
	if e != nil {
		return nil, e
	}
	return v.Open("muse-bridge-backup-v1", b[16:])
}
func NewTOTPSecret() string {
	b := make([]byte, 20)
	_, e := rand.Read(b)
	if e != nil {
		panic("system random source unavailable")
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}
func VerifyTOTP(secret, code string, now time.Time) bool {
	if len(code) != 6 {
		return false
	}
	key, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if e != nil {
		return false
	}
	for d := -1; d <= 1; d++ {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(now.Unix()/30+int64(d)))
		mac := hmac.New(sha1.New, key)
		_, _ = mac.Write(b[:])
		h := mac.Sum(nil)
		o := h[len(h)-1] & 15
		n := (binary.BigEndian.Uint32(h[o:o+4]) & 0x7fffffff) % 1000000
		if subtle.ConstantTimeCompare([]byte(fmt.Sprintf("%06d", n)), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(hatch_[a-z_]+\s*[=:]\s*)[^\s;,"}]+`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)[^\s,"}]+`),
	regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9_]{10,}|github_pat_[A-Za-z0-9_]{10,}|mprox_[A-Za-z0-9_-]{10,}|mbk_[A-Za-z0-9_-]{10,})`),
	regexp.MustCompile(`\b[0-9]{6,}:[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+@`),
}

type Redactor struct {
	mu     sync.RWMutex
	values map[string]struct{}
}

func NewRedactor() *Redactor { return &Redactor{values: map[string]struct{}{}} }
func (r *Redactor) Add(s string) {
	if len(s) < 4 {
		return
	}
	r.mu.Lock()
	r.values[s] = struct{}{}
	r.mu.Unlock()
}
func (r *Redactor) Text(s string) string {
	r.mu.RLock()
	for v := range r.values {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	r.mu.RUnlock()
	for _, p := range patterns {
		s = p.ReplaceAllString(s, "[REDACTED]")
	}
	if len(s) > 4096 {
		s = s[:4096] + "…"
	}
	return s
}
func Mask(s string) map[string]any {
	masked := ""
	if s != "" {
		masked = "••••••••"
	}
	return map[string]any{"configured": s != "", "masked": masked}
}
func ParseIDs(s string) map[int64]bool {
	out := map[int64]bool{}
	for _, v := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == ';' }) {
		n, e := strconv.ParseInt(v, 10, 64)
		if e == nil {
			out[n] = true
		}
	}
	return out
}
