package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const passwordIterations = 210000

// PBKDF2-HMAC-SHA256, one 32-byte block (RFC 8018 section 5.2).
func passwordKey(password string, salt []byte, iterations int) []byte {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write(salt)
	mac.Write([]byte{0, 0, 0, 1})
	u := mac.Sum(nil)
	key := append([]byte{}, u...)
	for i := 1; i < iterations; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(u[:0])
		for j := range key {
			key[j] ^= u[j]
		}
	}
	return key
}

func hashPassword(password string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, hex.EncodeToString(salt), hex.EncodeToString(passwordKey(password, salt, passwordIterations)))
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 10000 || iterations > 2000000 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil || len(salt) != 16 {
		return false
	}
	key, err := hex.DecodeString(parts[3])
	if err != nil || len(key) != 32 {
		return false
	}
	return subtle.ConstantTimeCompare(key, passwordKey(password, salt, iterations)) == 1
}
