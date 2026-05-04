package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

var encryptionKey []byte

func Init() {
	key := os.Getenv("ENCRYPTION_KEY")
	if key == "" {
		fmt.Println("WARNING: ENCRYPTION_KEY not set, using a random key (existing data will not decrypt)")
		encryptionKey = make([]byte, 32)
		rand.Read(encryptionKey)
		return
	}
	if len(key) != 32 {
		k := make([]byte, 32)
		copy(k, []byte(key))
		encryptionKey = k
	} else {
		encryptionKey = []byte(key)
	}
}

func Decrypt(ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	c, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, body := data[:nonceSize], data[nonceSize:]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func Encrypt(text string) (string, error) {
	c, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(c)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(text), nil)), nil
}
