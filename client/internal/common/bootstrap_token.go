package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const bootstrapTokenPrefix = "1cat1."

var bootstrapTokenKey = [32]byte{
	0x31, 0x63, 0x61, 0x74, 0x2d, 0x74, 0x75, 0x6e,
	0x6e, 0x65, 0x6c, 0x2d, 0x62, 0x6f, 0x6f, 0x74,
	0x73, 0x74, 0x72, 0x61, 0x70, 0x2d, 0x6b, 0x65,
	0x79, 0x2d, 0x76, 0x31, 0x2d, 0x30, 0x30, 0x31,
}

type BootstrapTokenPayload struct {
	ServerAddr  string `json:"server_addr"`
	NodeName    string `json:"node_name,omitempty"`
	AccessToken string `json:"access_token"`
	IssuedAt    int64  `json:"issued_at"`
}

func EncodeBootstrapToken(payload BootstrapTokenPayload) (string, error) {
	payload.ServerAddr = strings.TrimSpace(payload.ServerAddr)
	payload.NodeName = strings.TrimSpace(payload.NodeName)
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	if payload.ServerAddr == "" {
		return "", fmt.Errorf("server_addr is required")
	}
	if payload.NodeName == "" {
		return "", fmt.Errorf("node_name is required")
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("access_token is required")
	}
	if payload.IssuedAt == 0 {
		payload.IssuedAt = time.Now().Unix()
	}

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(bootstrapTokenKey[:])
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	packed := append(nonce, ciphertext...)
	return bootstrapTokenPrefix + base64.RawURLEncoding.EncodeToString(packed), nil
}

func DecodeBootstrapToken(token string) (BootstrapTokenPayload, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, bootstrapTokenPrefix) {
		return BootstrapTokenPayload{}, fmt.Errorf("invalid bootstrap token prefix")
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, bootstrapTokenPrefix))
	if err != nil {
		return BootstrapTokenPayload{}, fmt.Errorf("decode bootstrap token: %w", err)
	}

	block, err := aes.NewCipher(bootstrapTokenKey[:])
	if err != nil {
		return BootstrapTokenPayload{}, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return BootstrapTokenPayload{}, err
	}

	if len(raw) < gcm.NonceSize() {
		return BootstrapTokenPayload{}, fmt.Errorf("bootstrap token payload is too short")
	}

	nonce := raw[:gcm.NonceSize()]
	ciphertext := raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return BootstrapTokenPayload{}, fmt.Errorf("decrypt bootstrap token: %w", err)
	}

	var payload BootstrapTokenPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return BootstrapTokenPayload{}, fmt.Errorf("decode bootstrap token payload: %w", err)
	}

	payload.ServerAddr = strings.TrimSpace(payload.ServerAddr)
	payload.NodeName = strings.TrimSpace(payload.NodeName)
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	if payload.ServerAddr == "" || payload.AccessToken == "" {
		return BootstrapTokenPayload{}, fmt.Errorf("bootstrap token is missing required fields")
	}

	return payload, nil
}
