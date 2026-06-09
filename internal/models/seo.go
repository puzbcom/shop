package models

import "time"

type SEOMeta struct {
	ID            uint   `gorm:"primaryKey"`
	ModelType     string `gorm:"size:50;uniqueIndex:idx_seo_model"`
	ModelID       uint   `gorm:"uniqueIndex:idx_seo_model"`
	FocusKeyword  string `gorm:"size:255"`
	OGTitle       string `gorm:"size:255"`
	OGDescription string `gorm:"type:text"`
	OGImage       string `gorm:"size:500"`
	CanonicalURL  string `gorm:"size:500"`
	RobotsIndex   int    `gorm:"default:1"`
	RobotsFollow  int    `gorm:"default:1"`
	SchemaType    string `gorm:"size:50"`
	SEOScore      int    `gorm:"default:0"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (SEOMeta) TableName() string { return "seo_meta" }
