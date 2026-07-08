package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go-base-agent/internal/framework/config"

	"github.com/pgvector/pgvector-go"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type slogWriter struct{}

func (w slogWriter) Printf(format string, args ...interface{}) {
	slog.Default().Info(fmt.Sprintf(format, args...), "layer", "gorm")
}

// NewDB 创建 GORM 数据库连接，注册 pgvector 类型。
func NewDB(cfg config.DatabaseConfig) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger: gormlogger.New(slogWriter{}, gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	slog.Info("database connected",
		"host", cfg.Host,
		"port", cfg.Port,
		"dbname", cfg.Name,
	)

	_ = pgvector.Vector{}
	return db, nil
}

// Close 关闭数据库连接。
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}
	return sqlDB.Close()
}

// Ping 检查数据库连通性。
func Ping(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}
	return sqlDB.PingContext(ctx)
}
