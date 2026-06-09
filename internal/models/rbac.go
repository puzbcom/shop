package models

import "time"

// Spatie-compatible RBAC tables — model_type uses "App\Models\User" to match PHP side.

type Role struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:191;uniqueIndex:roles_name_guard_name_unique,composite:name_guard"`
	GuardName string `gorm:"size:191;uniqueIndex:roles_name_guard_name_unique,composite:name_guard"`
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

func (Role) TableName() string { return "roles" }

type Permission struct {
	ID        uint    `gorm:"primaryKey"`
	Name      string  `gorm:"size:191"`
	Section   *string `gorm:"size:50"`
	GuardName string  `gorm:"size:191"`
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

func (Permission) TableName() string { return "permissions" }

type RoleHasPermission struct {
	PermissionID uint `gorm:"primaryKey"`
	RoleID       uint `gorm:"primaryKey"`
}

func (RoleHasPermission) TableName() string { return "role_has_permissions" }

type ModelHasRole struct {
	RoleID    uint64 `gorm:"primaryKey"`
	ModelID   uint64 `gorm:"primaryKey"`
	ModelType string `gorm:"primaryKey;size:191"`
}

func (ModelHasRole) TableName() string { return "model_has_roles" }

type ModelHasPermission struct {
	PermissionID uint64 `gorm:"primaryKey"`
	ModelID      uint64 `gorm:"primaryKey"`
	ModelType    string `gorm:"primaryKey;size:191"`
}

func (ModelHasPermission) TableName() string { return "model_has_permissions" }

type RoleTranslation struct {
	ID        uint `gorm:"primaryKey"`
	RoleID    uint
	Lang      string `gorm:"size:100"`
	Name      string `gorm:"size:50"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (RoleTranslation) TableName() string { return "role_translations" }
