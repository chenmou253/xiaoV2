package model

import "time"

type Student struct {
	ID           uint64    `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"size:254;not null;uniqueIndex" json:"email"`
	PasswordHash string    `gorm:"size:255;not null" json:"-"`
	Verified     bool      `gorm:"not null;default:false" json:"verified"`
	Active       bool      `gorm:"not null;default:true" json:"active"`
	CreatedAt    time.Time `json:"created_at"`
}

type Admin struct {
	ID           uint64    `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"size:254;not null;uniqueIndex" json:"email"`
	PasswordHash string    `gorm:"size:255;not null" json:"-"`
	Verified     bool      `gorm:"not null;default:true" json:"verified"`
	Active       bool      `gorm:"not null;default:true" json:"active"`
	CreatedAt    time.Time `json:"created_at"`
}

type Role struct {
	ID          uint64 `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"size:40;not null;uniqueIndex" json:"name"`
	Description string `gorm:"size:255;not null;default:''" json:"description"`
	BuiltIn     bool   `gorm:"not null;default:false" json:"built_in"`
}
type Permission struct {
	Code        string `gorm:"size:64;primaryKey" json:"code"`
	Description string `gorm:"size:255;not null" json:"description"`
}
type RolePermission struct {
	RoleID         uint64 `gorm:"primaryKey" json:"role_id"`
	PermissionCode string `gorm:"size:64;primaryKey" json:"permission_code"`
}
type AdminRole struct {
	AdminID uint64 `gorm:"primaryKey" json:"admin_id"`
	RoleID  uint64 `gorm:"primaryKey" json:"role_id"`
}

type StudentSession struct {
	TokenHash string    `gorm:"size:64;primaryKey"`
	AccountID uint64    `gorm:"not null;index"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}
type AdminSession struct {
	TokenHash string    `gorm:"size:64;primaryKey"`
	AccountID uint64    `gorm:"not null;index"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}
type StudentEmailToken struct {
	TokenHash string    `gorm:"size:64;primaryKey"`
	AccountID uint64    `gorm:"not null;index"`
	Purpose   string    `gorm:"size:16;not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}
type AdminEmailToken struct {
	TokenHash string    `gorm:"size:64;primaryKey"`
	AccountID uint64    `gorm:"not null;index"`
	Purpose   string    `gorm:"size:16;not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}
type AuthThrottle struct {
	ID        string    `gorm:"size:191;primaryKey"`
	Attempts  int       `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
}

type SiteSetting struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	DictCode  string    `gorm:"size:64;not null;uniqueIndex:uk_dict_item,priority:1;index:idx_dict_code_status_sort,priority:1" json:"dict_code"`
	ItemLabel string    `gorm:"size:100;not null" json:"item_label"`
	ItemValue string    `gorm:"size:100;not null;uniqueIndex:uk_dict_item,priority:2" json:"item_value"`
	Sort      int       `gorm:"not null;default:0;index:idx_dict_code_status_sort,priority:3" json:"sort"`
	Status    int8      `gorm:"type:tinyint;not null;default:1;index:idx_dict_code_status_sort,priority:2" json:"status"`
	Remark    *string   `gorm:"size:500" json:"remark"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type AuditLog struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	ActorID   uint64    `gorm:"not null;index" json:"actor_id"`
	Action    string    `gorm:"size:80;not null;index" json:"action"`
	Target    string    `gorm:"size:191;not null" json:"target"`
	Detail    string    `gorm:"type:json;not null" json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}
type PageVersion struct {
	ID        uint64 `gorm:"primaryKey"`
	PageID    uint64 `gorm:"not null;index"`
	Version   uint64 `gorm:"not null"`
	Content   string `gorm:"type:json;not null"`
	Title     string `gorm:"size:255"`
	Unit      string `gorm:"size:255"`
	ActorID   uint64 `gorm:"not null"`
	CreatedAt time.Time
}

type TextbookDraft struct {
	ID                 string `gorm:"size:36;primaryKey" json:"id"`
	BookID             string `gorm:"size:80;not null;index" json:"book_id"`
	Title              string `gorm:"size:200;not null" json:"title"`
	Grade              int    `gorm:"not null" json:"grade"`
	Term               string `gorm:"size:20;not null" json:"term"`
	Edition            string `gorm:"size:200;not null" json:"edition"`
	AmericanEnabled    bool   `gorm:"not null;default:true" json:"american_enabled"`
	BritishEnabled     bool   `gorm:"not null;default:true" json:"british_enabled"`
	AmericanVoiceID    string `gorm:"size:80;not null;default:'aiden'" json:"american_voice_id"`
	BritishVoiceID     string `gorm:"size:80;not null;default:'ryan'" json:"british_voice_id"`
	AudioConfigVersion uint64 `gorm:"not null;default:1" json:"audio_config_version"`
	OCRModel           string `gorm:"size:64;not null;default:'local-paddleocr'" json:"ocr_model"`
	TTSModel           string `gorm:"size:64;not null;default:'local-qwen3-tts'" json:"tts_model"`
	TTSVoice           string `gorm:"size:100;not null;default:'aiden'" json:"tts_voice"`
	// SourcePageCount is the number of pages in the uploaded PDF.  It keeps
	// the editor on a strictly sequential, one-page-at-a-time workflow.
	SourcePageCount int       `gorm:"not null;default:0" json:"source_page_count"`
	Status          string    `gorm:"size:24;not null;index" json:"status"`
	Version         uint64    `gorm:"not null;default:1" json:"version"`
	ReviewNote      string    `gorm:"type:text" json:"review_note"`
	CreatedBy       uint64    `gorm:"not null" json:"created_by"`
	UpdatedBy       uint64    `gorm:"not null" json:"updated_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type TextbookDraftPage struct {
	ID           uint64 `gorm:"primaryKey" json:"id"`
	DraftID      string `gorm:"size:36;not null;uniqueIndex:uidx_draft_page" json:"draft_id"`
	Position     int    `gorm:"not null;uniqueIndex:uidx_draft_page" json:"position"`
	PrintedPage  *int   `json:"printed_page"`
	Title        string `gorm:"size:255;not null" json:"title"`
	Unit         string `gorm:"size:255;not null" json:"unit"`
	ImagePath    string `gorm:"size:500;not null" json:"-"`
	Content      string `gorm:"type:json;not null" json:"content"`
	Preview      bool   `gorm:"not null;default:false" json:"preview"`
	Checked      bool   `gorm:"not null;default:false" json:"checked"`
	AudioChecked bool   `gorm:"not null;default:false" json:"audio_checked"`
	// Model fields are page snapshots. A fully reviewed page keeps these values
	// even when the draft default changes for later/unreviewed pages.
	OCRModel  string    `gorm:"size:64;not null;default:''" json:"ocr_model"`
	TTSModel  string    `gorm:"size:64;not null;default:''" json:"tts_model"`
	TTSVoice  string    `gorm:"size:100;not null;default:''" json:"tts_voice"`
	Version   uint64    `gorm:"not null;default:1" json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}
type TextbookJob struct {
	ID      uint64 `gorm:"primaryKey" json:"id"`
	DraftID string `gorm:"size:36;not null;index" json:"draft_id"`
	Kind    string `gorm:"size:16;not null" json:"kind"`
	// Page is the source-PDF page handled by this job.  Zero is retained for
	// compatibility with jobs created before the sequential workflow.
	Page        int        `gorm:"not null;default:0" json:"page"`
	ItemID      string     `gorm:"size:191;not null;default:'';index" json:"item_id"`
	Accent      string     `gorm:"size:8;not null;default:''" json:"accent"`
	VoiceID     string     `gorm:"size:80;not null;default:''" json:"voice_id"`
	ModelID     string     `gorm:"size:64;not null;default:'';index" json:"model_id"`
	Provider    string     `gorm:"size:32;not null;default:''" json:"provider"`
	RequestID   string     `gorm:"size:191;not null;default:''" json:"request_id"`
	Status      string     `gorm:"size:32;not null;index;index:idx_job_claim,priority:1" json:"status"`
	Progress    int        `gorm:"not null" json:"progress"`
	Total       int        `gorm:"not null" json:"total"`
	Attempts    int        `gorm:"not null" json:"attempts"`
	Priority    int        `gorm:"not null;default:30;index:idx_job_claim,priority:2" json:"priority"`
	AvailableAt *time.Time `gorm:"index:idx_job_claim,priority:3" json:"available_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Error       string     `gorm:"type:text" json:"error"`
	CreatedAt   time.Time  `gorm:"index:idx_job_claim,priority:4" json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TextbookAudioItem is the current, queryable state for one logical audio
// item. WAV files remain on disk; the database stores only state and paths.
type TextbookAudioItem struct {
	ID                uint64     `gorm:"primaryKey" json:"id"`
	DraftID           string     `gorm:"size:36;not null;uniqueIndex:uk_audio_current,priority:1;index:idx_audio_page_status,priority:1" json:"draft_id"`
	Page              int        `gorm:"not null;uniqueIndex:uk_audio_current,priority:2;index:idx_audio_page_status,priority:2" json:"page"`
	ItemID            string     `gorm:"size:191;not null;uniqueIndex:uk_audio_current,priority:3" json:"item_id"`
	SegmentID         string     `gorm:"size:191;not null;default:''" json:"segment_id"`
	ItemType          string     `gorm:"size:16;not null" json:"item_type"`
	WordIndex         *int       `json:"word_index,omitempty"`
	Text              string     `gorm:"type:text;not null" json:"text"`
	Context           string     `gorm:"type:text" json:"context"`
	Accent            string     `gorm:"size:8;not null;uniqueIndex:uk_audio_current,priority:4" json:"accent"`
	ModelID           string     `gorm:"size:64;not null;default:''" json:"model_id"`
	Provider          string     `gorm:"size:32;not null;default:''" json:"provider"`
	VoiceID           string     `gorm:"size:80;not null;default:''" json:"voice_id"`
	Status            string     `gorm:"size:32;not null;index:idx_audio_page_status,priority:3;index:idx_audio_review_queue,priority:1" json:"status"`
	Active            bool       `gorm:"not null;default:true" json:"active"`
	SourcePageVersion uint64     `gorm:"not null;default:1" json:"source_page_version"`
	AttemptCount      int        `gorm:"not null;default:0" json:"attempt_count"`
	QAScore           *float64   `gorm:"type:decimal(8,6)" json:"qa_score,omitempty"`
	FailureReasons    string     `gorm:"type:json" json:"failure_reasons"`
	AudioPath         string     `gorm:"size:500;not null;default:''" json:"audio_path"`
	CandidatePath     string     `gorm:"size:500;not null;default:''" json:"candidate_path"`
	FileSHA256        string     `gorm:"size:64;not null;default:''" json:"file_sha256"`
	FileSize          uint64     `gorm:"not null;default:0" json:"file_size"`
	DurationMS        uint       `gorm:"not null;default:0" json:"duration_ms"`
	GenerationVersion string     `gorm:"size:64;not null;default:''" json:"generation_version"`
	RequestID         string     `gorm:"size:191;not null;default:''" json:"request_id"`
	ActiveJobID       *uint64    `gorm:"index" json:"active_job_id,omitempty"`
	Revision          uint64     `gorm:"not null;default:1" json:"revision"`
	ReviewedBy        *uint64    `json:"reviewed_by,omitempty"`
	ReviewedAt        *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `gorm:"index:idx_audio_review_queue,priority:2" json:"updated_at"`
}

// TextbookAudioAttempt retains immutable generation and QA history while the
// current item table stays compact.
type TextbookAudioAttempt struct {
	ID                uint64    `gorm:"primaryKey" json:"id"`
	AudioItemID       uint64    `gorm:"not null;uniqueIndex:uk_audio_attempt,priority:1;index" json:"audio_item_id"`
	JobID             *uint64   `gorm:"index" json:"job_id,omitempty"`
	GenerationVersion string    `gorm:"size:64;not null;uniqueIndex:uk_audio_attempt,priority:2" json:"generation_version"`
	AttemptNo         int       `gorm:"not null;uniqueIndex:uk_audio_attempt,priority:3" json:"attempt_no"`
	ModelID           string    `gorm:"size:64;not null;default:''" json:"model_id"`
	Provider          string    `gorm:"size:32;not null;default:''" json:"provider"`
	VoiceID           string    `gorm:"size:80;not null;default:''" json:"voice_id"`
	RequestID         string    `gorm:"size:191;not null;default:''" json:"request_id"`
	Result            string    `gorm:"size:32;not null" json:"result"`
	QAScore           *float64  `gorm:"type:decimal(8,6)" json:"qa_score,omitempty"`
	FailureReasons    string    `gorm:"type:json" json:"failure_reasons"`
	QADetail          string    `gorm:"type:json" json:"qa_detail"`
	CandidatePath     string    `gorm:"size:500;not null;default:''" json:"candidate_path"`
	FileSHA256        string    `gorm:"size:64;not null;default:''" json:"file_sha256"`
	FileSize          uint64    `gorm:"not null;default:0" json:"file_size"`
	DurationMS        uint      `gorm:"not null;default:0" json:"duration_ms"`
	GenerationMS      uint      `gorm:"not null;default:0" json:"generation_ms"`
	CreatedAt         time.Time `json:"created_at"`
}
