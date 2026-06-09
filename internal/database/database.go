// Package database opens and configures the GORM connection to MySQL.
package database

import (
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"mall/internal/config"
)

// Open connects to the application database via GORM and configures the pool.
func Open(cfg config.Config) (*gorm.DB, error) {
	level := gormlogger.Warn
	if cfg.IsDev() {
		level = gormlogger.Info
	}
	db, err := gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(level),
		DisableForeignKeyConstraintWhenMigrating:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("open gorm: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(time.Hour)
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}
