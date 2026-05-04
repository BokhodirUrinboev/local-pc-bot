package models

import (
	"time"

	"gorm.io/gorm"
)

// User — web-ssh-backend dagi sxema bilan AYNAN bir xil.
// Bot bu jadvalga yozmaydi (faqat Google OAuth callback FirstOrCreate uchun).
type User struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	GoogleID  string         `gorm:"uniqueIndex;not null" json:"google_id"`
	Email     string         `gorm:"uniqueIndex;not null" json:"email"`
	Name      string         `json:"name"`
	AvatarURL string         `json:"avatar_url"`
	Servers   []Server       `gorm:"foreignKey:UserID" json:"servers,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

type Folder struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UserID    uint           `gorm:"index;not null" json:"user_id"`
	Name      string         `gorm:"not null" json:"name"`
	Servers   []Server       `gorm:"foreignKey:FolderID" json:"servers,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

type Server struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	UserID          uint           `gorm:"index;not null" json:"user_id"`
	FolderID        *uint          `gorm:"index" json:"folder_id"`
	Name            string         `gorm:"not null" json:"name"`
	Host            string         `gorm:"not null" json:"host"`
	Port            int            `gorm:"default:22" json:"port"`
	Username        string         `gorm:"not null" json:"username"`
	AuthType        string         `gorm:"not null" json:"auth_type"`
	EncryptedSecret string         `gorm:"not null" json:"-"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}
