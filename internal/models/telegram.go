package models

import "time"

// TelegramUser — bot Telegram foydalanuvchisi va web-ssh User o'rtasidagi bog'lanish.
// Faqat shu jadval bot tomonidan AutoMigrate qilinadi.
type TelegramUser struct {
	ID         uint      `gorm:"primaryKey"`
	TelegramID int64     `gorm:"uniqueIndex;not null"`
	UserID     uint      `gorm:"index;not null"`
	User       User      `gorm:"foreignKey:UserID"`
	Username   string
	LinkedAt   time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
