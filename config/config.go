package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Port           string
	DatabaseURL    string
	RedisURL       string
	RedisPassword  string
	RedisDB        int
	JWTSecret      string
	WSTokenTTL     int // WebSocket Token 有效期（秒）
	MaxVideoSizeMB int // 视频文件最大尺寸（MB）

	OSS struct {
		Endpoint        string
		AccessKeyID     string
		AccessKeySecret string
		Bucket          string
	}
}

func Load() *Config {
	// 加载 .env 文件
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	redisDB, _ := strconv.Atoi(getEnv("REDIS_DB", "0"))
	wsTokenTTL, _ := strconv.Atoi(getEnv("WS_TOKEN_TTL", "300"))
	maxVideoSizeMB, _ := strconv.Atoi(getEnv("MAX_VIDEO_SIZE_MB", "5"))

	cfg := &Config{
		Port:           getEnv("PORT", "8080"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		RedisURL:       getEnv("REDIS_URL", "localhost:6379"),
		RedisPassword:  os.Getenv("REDIS_PASSWORD"),
		RedisDB:        redisDB,
		JWTSecret:      os.Getenv("JWT_SECRET"),
		WSTokenTTL:     wsTokenTTL,
		MaxVideoSizeMB: maxVideoSizeMB,
	}

	cfg.OSS.Endpoint = os.Getenv("OSS_ENDPOINT")
	cfg.OSS.AccessKeyID = os.Getenv("OSS_ACCESS_KEY_ID")
	cfg.OSS.AccessKeySecret = os.Getenv("OSS_ACCESS_KEY_SECRET")
	cfg.OSS.Bucket = getEnv("OSS_BUCKET", "dinq")

	return cfg
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
