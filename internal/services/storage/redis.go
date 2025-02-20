package storage

import (
	"context"
	"fmt"
	"log"
	"math"
	"oneinstack/internal/models"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisOP struct {
	ID       int64
	Addr     string
	Port     string
	Root     string
	Password string
	Type     string
	DB       *redis.Client
}

func NewRedisOP(p *models.Storage) *RedisOP {
	return &RedisOP{
		ID:       p.ID,
		Addr:     p.Addr,
		Port:     p.Port,
		Root:     p.Root,
		Password: p.Password,
		Type:     p.Type,
		DB:       nil,
	}
}

func (s *RedisOP) Connet() error {
	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%v:%v", s.Addr, s.Port),
		Password: s.Password, // no password set
		DB:       0,          // use default DB
	})
	_, err := rdb.SetNX(context.Background(), "test_key", "value", 10*time.Second).Result()
	if err != nil {
		return err
	}
	s.DB = rdb
	return nil
}

func (s *RedisOP) Sync() error {
	//redis的库无需存储，采用实时获取
	return nil

}

func (s *RedisOP) GetLibs() ([]models.Library, error) {
	// 使用 context
	ctx := context.Background()

	// 获取配置中的数据库数量
	config, err := s.DB.ConfigGet(ctx, "databases").Result()
	if err != nil {
		log.Fatalf("获取配置失败: %v\n", err)
	}

	// 输出数据库数量
	dbCount, ok := config["databases"]
	if !ok {
		dbCount = "16"
	}
	parseInt, err := strconv.ParseInt(dbCount, 10, 64)
	if err != nil {
		return nil, err
	}
	ls := []models.Library{}
	// 遍历数据库，检查是否有数据
	for db := 0; db < int(parseInt); db++ { // 假设最多 16 个数据库
		// 切换到指定数据库
		s.DB.Options().DB = db // 使用 SetDB 选择数据库

		// 获取当前数据库的键数量
		//keyCount, err := s.DB.DBSize(ctx).Result()
		//if err != nil {
		//	return err
		//}
		l := models.Library{
			PID:      s.ID,
			Name:     fmt.Sprintf("%v", db),
			User:     "",
			Password: "",
			//Capacity: fmt.Sprintf("%v", keyCount-1),
			PAddr: fmt.Sprintf("%s:%v", s.Addr, s.Port),
			Type:  s.Type,
		}
		ls = append(ls, l)
	}
	return ls, nil
}

// KeyInfo holds information about a Redis key.
type KeyInfo struct {
	Key        string `json:"key"`
	Type       string `json:"type"`
	Length     int64  `json:"length"`
	Expiration int64  `json:"expiration"` // TTL in seconds, -1 means no expiration, -2 means key doesn't exist
}

// PaginatedKeysInfo holds paginated results for Redis keys.
type PaginatedKeysInfo struct {
	Keys       []KeyInfo `json:"keys"`
	Total      int       `json:"total"`
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
	TotalPages int       `json:"total_pages"`
}

// GetPaginatedKeyInfo retrieves paginated key information for a Redis database.
func (s *RedisOP) GetPaginatedKeyInfo(ctx context.Context, db int, pattern string, page, pageSize int) (*PaginatedKeysInfo, error) {
	if pageSize <= 0 {
		pageSize = 10 // 默认每页显示10条记录
	}
	if page <= 0 {
		page = 1 // 默认从第一页开始
	}

	// 使用 SCAN 遍历键
	var allKeys []string
	cursor := uint64(0)
	s.DB.Options().DB = db
	for {
		keys, nextCursor, err := s.DB.Scan(ctx, cursor, pattern, 100).Result() // 每次最多扫描 1000 个键
		if err != nil {
			return nil, fmt.Errorf("failed to scan keys: %w", err)
		}
		allKeys = append(allKeys, keys...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	totalKeys := len(allKeys)

	// 计算分页范围
	offset := (page - 1) * pageSize
	if offset >= totalKeys {
		return &PaginatedKeysInfo{
			Keys:       []KeyInfo{},
			Total:      totalKeys,
			Page:       page,
			PageSize:   pageSize,
			TotalPages: int(math.Ceil(float64(totalKeys) / float64(pageSize))),
		}, nil
	}

	end := offset + pageSize
	if end > totalKeys {
		end = totalKeys
	}

	// 分页后的键
	keysPage := allKeys[offset:end]

	// 获取每个键的详细信息
	var keysInfo []KeyInfo
	for _, key := range keysPage {
		keyType, err := s.DB.Type(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to get key type for key %s: %w", key, err)
		}

		// 根据键类型获取长度
		var length int64
		switch keyType {
		case "string":
			length, err = s.DB.StrLen(ctx, key).Result()
		case "hash":
			length, err = s.DB.HLen(ctx, key).Result()
		case "list":
			length, err = s.DB.LLen(ctx, key).Result()
		case "set":
			length, err = s.DB.SCard(ctx, key).Result()
		case "zset":
			length, err = s.DB.ZCard(ctx, key).Result()
		default:
			length = 0
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get length for key %s: %w", key, err)
		}

		// 获取键的 TTL
		ttl, err := s.DB.TTL(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to get TTL for key %s: %w", key, err)
		}
		ttlSeconds := int64(ttl.Seconds())
		if ttlSeconds < -1 {
			ttlSeconds = -2 // Key does not exist
		}

		keysInfo = append(keysInfo, KeyInfo{
			Key:        key,
			Type:       keyType,
			Length:     length,
			Expiration: ttlSeconds,
		})
	}

	return &PaginatedKeysInfo{
		Keys:       keysInfo,
		Total:      totalKeys,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: int(math.Ceil(float64(totalKeys) / float64(pageSize))),
	}, nil
}

func (s *RedisOP) CreateLibrary(lb *models.Library) error {
	return nil
}
