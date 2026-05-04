package db

import (
	"log"
	"os"

	"remofy-bot/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func Init() {
	dsn := os.Getenv("DB_PATH")
	if dsn == "" {
		log.Fatalf("DB_PATH environment variable is not set")
	}

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	log.Println("Database connection established")

	if err := DB.AutoMigrate(&models.TelegramUser{}); err != nil {
		log.Fatalf("Failed to migrate telegram_users: %v", err)
	}
	log.Println("telegram_users table ready")
}
