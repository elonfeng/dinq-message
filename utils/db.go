package utils

import (
	"context"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// CustomLogger 自定义 GORM 日志器：只打印慢查询和真实错误
type CustomLogger struct {
	SlowThreshold time.Duration // 慢查询阈值
}

func (l *CustomLogger) LogMode(level logger.LogLevel) logger.Interface {
	return l
}

func (l *CustomLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	// 不打印 Info 日志
}

func (l *CustomLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	// 不打印 Warn 日志
}

func (l *CustomLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	// 只打印真实错误，忽略 "record not found"
	if msg != "record not found" {
		log.Printf("[GORM Error] "+msg, data...)
	}
}

func (l *CustomLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	sql, rows := fc()

	// 只打印慢查询（超过阈值）或真实错误
	if err != nil && err.Error() != "record not found" {
		// 真实错误
		log.Printf("[GORM Error] %s [%v] [rows:%d] %s", err, elapsed, rows, sql)
	} else if elapsed >= l.SlowThreshold {
		// 慢查询
		log.Printf("[SLOW SQL] [%v] [rows:%d] %s", elapsed, rows, sql)
	}
}

// InitDB 初始化数据库连接
func InitDB(databaseURL string) error {
	var err error
	DB, err = gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: &CustomLogger{
			SlowThreshold: 100 * time.Millisecond, // 慢查询阈值：100ms
		},
	})
	if err != nil {
		return err
	}

	// 获取底层的 sql.DB 以配置连接池
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}

	// 连接池配置
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetMaxIdleConns(20)

	log.Println("✅ Database connected")
	return nil
}

// GetDB 获取数据库连接
func GetDB() *gorm.DB {
	return DB
}

// CloseDB 关闭数据库连接
func CloseDB() error {
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
