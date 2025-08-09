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

	// 创建一个新的viper实例来读取配置文件
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./")

	// 尝试读取配置文件
	if err := v.ReadInConfig(); err == nil {
		if jwtSecret := v.GetString("system.jwtSecret"); jwtSecret != "" {
			// 如果是hex编码的字符串，尝试解码
			if decoded, err := hex.DecodeString(jwtSecret); err == nil && len(decoded) >= 32 {
				return decoded, nil
			}
			// 如果不是hex编码或者解码失败，直接使用原字符串
			return []byte(jwtSecret), nil
		}
	}

	// 如果都没有，生成一个随机密钥并保存到配置
	key, err := generateRandomKey(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate JWT key: %v", err)
	}

	// 将生成的密钥保存到配置文件
	keyHex := hex.EncodeToString(key)
	v.Set("system.jwtSecret", keyHex)

	// 保存配置到文件
	if err := v.WriteConfig(); err != nil {
		fmt.Printf("Warning: Failed to save JWT key to config file: %v\n", err)
		fmt.Printf("Please manually set JWT_SECRET_KEY environment variable: %s\n", keyHex)
	} else {
		fmt.Printf("Auto-generated JWT key saved to config file\n")
	}

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
