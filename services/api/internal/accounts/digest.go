package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// CredentialDigester 使用认证专用 pepper 派生数据库中的不可逆摘要。
// pepper 与 JWT 签名密钥相互独立，并为手机号、验证码等字段使用不同的 domain 前缀，
// 防止某一列的摘要被复制后在另一种凭证场景中当作合法值重用。
type CredentialDigester struct{ pepper []byte }

func NewCredentialDigester(pepper string) (*CredentialDigester, error) {
	if len([]byte(pepper)) < 32 {
		return nil, fmt.Errorf("authentication pepper must be at least 32 bytes")
	}
	return &CredentialDigester{pepper: []byte(pepper)}, nil
}

// PhoneHash 生成稳定的手机号查询摘要；数据库无需保存手机号明文即可查找同一账户。
func (d *CredentialDigester) PhoneHash(phone string) string {
	return d.sum("lingow.phone.v2\x00" + phone)
}

// CodeHash 将 challenge ID 与验证码共同纳入摘要，使同一个验证码不能跨挑战重放。
func (d *CredentialDigester) CodeHash(challengeID, code string) string {
	return d.sum("lingow.otp.v2\x00" + challengeID + "\x00" + code)
}

// EncryptLegacyPhoneHash 加密一次性兼容查询值，避免旧版手机号 SHA-256 摘要以明文形式进入新挑战表。
// 该值只在 v2 挑战有效期内使用，登录成功后用于惰性迁移仍保存旧摘要的账户。
func (d *CredentialDigester) EncryptLegacyPhoneHash(legacyHash string) (string, error) {
	block, err := aes.NewCipher(d.encryptionKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(legacyHash), nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// DecryptLegacyPhoneHash 只在验证码已成功消费后恢复兼容查询值。
// 解密或完整性校验失败时登录必须终止，不能降级为不安全的明文查询。
func (d *CredentialDigester) DecryptLegacyPhoneHash(encoded string) (string, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(d.encryptionKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", fmt.Errorf("legacy phone lookup ciphertext is invalid")
	}
	plain, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (d *CredentialDigester) sum(value string) string {
	mac := hmac.New(sha256.New, d.pepper)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func (d *CredentialDigester) encryptionKey() []byte {
	mac := hmac.New(sha256.New, d.pepper)
	_, _ = mac.Write([]byte("lingow.phone.legacy-encryption.v1"))
	return mac.Sum(nil)
}
