package utils

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

type Claims struct {
	Id       int64  `json:"id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// getJWTKey 安全获取JWT密钥
func getJWTKey() ([]byte, error) {
	// 优先从环境变量获取
	if key := os.Getenv("JWT_SECRET_KEY"); key != "" {
		return []byte(key), nil
	}

	// 从配置文件获取
	if jwtSecret := viper.GetString("system.jwtSecret"); jwtSecret != "" {
		return []byte(jwtSecret), nil
	}

	// 如果都没有，生成一个随机密钥并保存到配置
	key, err := generateRandomKey(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate JWT key: %v", err)
	}

	// 这里可以选择保存到配置文件或提示用户设置环境变量
	fmt.Printf("Warning: Using auto-generated JWT key. Please set JWT_SECRET_KEY environment variable for production use: %s\n", hex.EncodeToString(key))

	return key, nil
}

// generateRandomKey 生成随机密钥
func generateRandomKey(length int) ([]byte, error) {
	key := make([]byte, length)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func GenerateJWT(username string, id int64) (string, error) {
	jwtKey, err := getJWTKey()
	if err != nil {
		return "", err
	}

	expirationTime := time.Now().Add(24 * time.Hour)
	claims := &Claims{
		Id:       id,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "oneinstack-panel",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtKey)
}

func ValidateJWT(tokenStr string) (*Claims, error) {
	jwtKey, err := getJWTKey()
	if err != nil {
		return nil, err
	}

	claims := &Claims{}
	tkn, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
		// 验证签名方法
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtKey, nil
	})
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
