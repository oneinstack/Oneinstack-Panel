package utils

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	Id              int64  `json:"id"`
	Username        string `json:"username"`
	SessionID       string `json:"sid,omitempty"`
	SecurityVersion uint64 `json:"sv,omitempty"`
	jwt.RegisteredClaims
}

var configuredJWTKey struct {
	sync.RWMutex
	value []byte
}

func ConfigureJWTKey(key []byte) error {
	if len(key) < 32 {
		return errors.New("JWT key must contain at least 32 bytes")
	}
	configuredJWTKey.Lock()
	configuredJWTKey.value = append(configuredJWTKey.value[:0], key...)
	configuredJWTKey.Unlock()
	return nil
}

func getJWTKey() ([]byte, error) {
	if key := os.Getenv("JWT_SECRET_KEY"); key != "" {
		if len(key) < 32 {
			return nil, errors.New("JWT_SECRET_KEY must contain at least 32 bytes")
		}
		return []byte(key), nil
	}

	configuredJWTKey.RLock()
	defer configuredJWTKey.RUnlock()
	if len(configuredJWTKey.value) < 32 {
		return nil, errors.New("JWT key is not initialized")
	}
	return append([]byte(nil), configuredJWTKey.value...), nil
}

func GenerateJWT(username string, id int64) (string, error) {
	token, _, err := generateJWT(username, id, "", 0)
	return token, err
}

// GenerateSessionJWT creates a JWT bound to a persistent, revocable session.
func GenerateSessionJWT(username string, id int64, sessionID string, securityVersion uint64) (string, time.Time, error) {
	if sessionID == "" {
		return "", time.Time{}, errors.New("session id is required")
	}
	if securityVersion == 0 {
		securityVersion = 1
	}
	return generateJWT(username, id, sessionID, securityVersion)
}

func generateJWT(username string, id int64, sessionID string, securityVersion uint64) (string, time.Time, error) {
	jwtKey, err := getJWTKey()
	if err != nil {
		return "", time.Time{}, err
	}

	now := time.Now()
	expirationTime := now.Add(24 * time.Hour)
	claims := &Claims{
		Id: id, Username: username, SessionID: sessionID,
		SecurityVersion: securityVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "oneinstack-panel",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtKey)
	return signed, expirationTime, err
}

func ValidateJWT(tokenStr string) (*Claims, error) {
	jwtKey, err := getJWTKey()
	if err != nil {
		return nil, err
	}

	claims := &Claims{}
	tkn, err := jwt.ParseWithClaims(
		tokenStr,
		claims,
		func(token *jwt.Token) (interface{}, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return jwtKey, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("oneinstack-panel"),
	)
	if err != nil {
		return nil, err
	}
	if !tkn.Valid {
		return nil, errors.New("invalid token")
	}

	// 验证token是否过期
	if claims.ExpiresAt != nil && claims.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("token expired")
	}

	return claims, nil
}
