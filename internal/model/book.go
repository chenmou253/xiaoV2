package model

import "time"

type Book struct {
	ID              uint64 `gorm:"primaryKey" json:"-"`
	BookID          string `gorm:"size:80;not null;uniqueIndex:uidx_books_book_id" json:"book_id"`
	Title           string `gorm:"size:200;not null" json:"title"`
	Subtitle        string `gorm:"size:200;not null;default:''" json:"subtitle"`
	Description     string `gorm:"type:text" json:"description"`
	Publisher       string `gorm:"size:120;not null;default:''" json:"publisher"`
	Grade           string `gorm:"size:40;not null;default:''" json:"grade"`
	Semester        string `gorm:"size:40;not null;default:''" json:"semester"`
	Cover           string `gorm:"size:255;not null;default:''" json:"-"`
	Status          string `gorm:"size:20;not null;default:'draft';index" json:"status"`
	PageCount       int    `gorm:"not null;default:0" json:"page_count"`
	Sort            int    `gorm:"not null;default:0;index" json:"sort"`
	Revision        uint64 `gorm:"not null;default:1" json:"revision"`
	AmericanEnabled bool   `gorm:"not null;default:true" json:"american_enabled"`
	BritishEnabled  bool   `gorm:"not null;default:true" json:"british_enabled"`
	AmericanVoiceID string `gorm:"size:80;not null;default:'aiden'" json:"american_voice_id"`
	BritishVoiceID  string `gorm:"size:80;not null;default:'ryan'" json:"british_voice_id"`
	// Version zero identifies books that predate per-book audio settings. Their
	// existing manifest remains valid regardless of the historical speaker.
	AudioConfigVersion uint64    `gorm:"not null;default:0" json:"audio_config_version"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type BookPage struct {
	ID          uint64    `gorm:"primaryKey" json:"-"`
	BookID      string    `gorm:"size:80;not null;uniqueIndex:uidx_book_pages_book_position,priority:1;index" json:"book_id"`
	Position    int       `gorm:"not null;uniqueIndex:uidx_book_pages_book_position,priority:2" json:"position"`
	PrintedPage *int      `json:"printed_page"`
	PageGroup   string    `gorm:"size:24;not null;default:''" json:"page_group"`
	PageLabel   string    `gorm:"size:80;not null;default:''" json:"page_label"`
	Title       string    `gorm:"size:255;not null;default:''" json:"title"`
	Unit        string    `gorm:"size:255;not null;default:''" json:"unit"`
	ImagePath   string    `gorm:"size:255;not null" json:"-"`
	ContentPath string    `gorm:"size:255;not null" json:"-"`
	Interactive bool      `gorm:"not null;default:false" json:"interactive"`
	Preview     bool      `gorm:"not null;default:false" json:"preview"`
	Version     uint64    `gorm:"not null;default:1" json:"version"`
	CreatedAt   time.Time `json:"-"`
	UpdatedAt   time.Time `json:"-"`
}
