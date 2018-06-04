package libtwitch

import (
	"crypto/rand"
	"encoding/hex"
)

func makeSecret() (string, error) {
	bytes := make([]byte, 10)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
