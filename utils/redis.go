package utils

import (
	"context"
	"log"

	"github.com/redis/go-redis/v9"
)

var rdb *redis.Client

// InitRedis 初始化 Redis 连接
func InitRedis(url, password string, db int) error {
	rdb = redis.NewClient(&redis.Options{
		Addr:     url,
		Password: password,
		DB:       db,
	})

	// 测试连接
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	log.Println("Redis connected")
	return nil
}

// GetRedis 获取 Redis 客户端
func GetRedis() *redis.Client {
	return rdb
}

// CloseRedis 关闭 Redis 连接
func CloseRedis() error {
	if rdb != nil {
		return rdb.Close()
	}
	return nil
}
