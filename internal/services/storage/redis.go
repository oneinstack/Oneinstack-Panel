package storage

import (
	"context"
	"fmt"
	"math"
	"oneinstack/internal/models"
	"sort"
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

func (s *RedisOP) Connect() error {
	rdb := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%v:%v", s.Addr, s.Port),
		Username:     s.Root,
		Password:     s.Password,
		DB:           0,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		_ = rdb.Close()
		return err
	}
	s.DB = rdb
	return nil
}

func (s *RedisOP) Close() error {
	if s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *RedisOP) Sync() error {
	//redis的库无需存储，采用实时获取
	return nil

}

func (s *RedisOP) GetLibs() ([]models.Library, error) {
	// 使用 context
	ctx := context.Background()

	// 获取配置中的数据库数量
	dbCount := "16"
	config, err := s.DB.ConfigGet(ctx, "databases").Result()
	if err == nil {
		if configured, ok := config["databases"]; ok && configured != "" {
			dbCount = configured
		}
	}
	parseInt, err := strconv.ParseInt(dbCount, 10, 64)
	if err != nil {
		return nil, err
	}
	ls := []models.Library{}
	// 遍历数据库，检查是否有数据
	for db := 0; db < int(parseInt); db++ { // 假设最多 16 个数据库
		client := s.clientForDB(db)
		keyCount, err := client.DBSize(ctx).Result()
		_ = client.Close()
		if err != nil {
			return nil, fmt.Errorf("read Redis DB%d size: %w", db, err)
		}
		l := models.Library{
			PID:      s.ID,
			Name:     fmt.Sprintf("%v", db),
			User:     "",
			Password: "",
			Capacity: fmt.Sprintf("%d keys", keyCount),
			PAddr:    fmt.Sprintf("%s:%v", s.Addr, s.Port),
			Type:     s.Type,
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
	if pattern == "" {
		pattern = "*"
	}
	client := s.clientForDB(db)
	defer client.Close()
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 500).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to scan keys: %w", err)
		}
		allKeys = append(allKeys, keys...)
		if len(allKeys) > 100000 {
			return nil, fmt.Errorf("Redis key listing exceeds the 100000-key safety limit")
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	sort.Strings(allKeys)

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
		keyType, err := client.Type(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to get key type for key %s: %w", key, err)
		}

		// 根据键类型获取长度
		var length int64
		switch keyType {
		case "string":
			length, err = client.StrLen(ctx, key).Result()
		case "hash":
			length, err = client.HLen(ctx, key).Result()
		case "list":
			length, err = client.LLen(ctx, key).Result()
		case "set":
			length, err = client.SCard(ctx, key).Result()
		case "zset":
			length, err = client.ZCard(ctx, key).Result()
		default:
			length = 0
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get length for key %s: %w", key, err)
		}

		// 获取键的 TTL
		ttl, err := client.TTL(ctx, key).Result()
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
	return fmt.Errorf("Redis logical databases are configured by the Redis server and cannot be created from Panel")
}

func (s *RedisOP) DeleteLibrary(lb *models.Library) error {
	return fmt.Errorf("deleting or flushing a Redis logical database is not supported by this endpoint")
}

func (s *RedisOP) UpdateLibraryPassword(_ *models.Library, _ string) error {
	return fmt.Errorf("Redis logical databases do not have dedicated user passwords")
}

func (s *RedisOP) clientForDB(db int) *redis.Client {
	options := *s.DB.Options()
	options.DB = db
	return redis.NewClient(&options)
}
