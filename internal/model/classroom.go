package model

import "time"

type Teacher struct {
	ID                  uint64    `gorm:"primaryKey" json:"id"`
	ActivationEmailSent *bool     `gorm:"-" json:"activation_email_sent,omitempty"`
	Email               string    `gorm:"size:254;not null;uniqueIndex" json:"email"`
	PasswordHash        string    `gorm:"size:255;not null" json:"-"`
	Verified            bool      `gorm:"not null;default:false" json:"verified"`
	Active              bool      `gorm:"not null;default:true" json:"active"`
	DisplayName         string    `gorm:"size:120;not null" json:"display_name"`
	Avatar              string    `gorm:"size:500" json:"avatar"`
	Country             string    `gorm:"size:80" json:"country"`
	Timezone            string    `gorm:"size:80;not null" json:"timezone"`
	Bio                 string    `gorm:"type:text" json:"bio"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type TeacherSession struct {
	TokenHash string    `gorm:"size:64;primaryKey"`
	AccountID uint64    `gorm:"not null;index"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}

type TeacherEmailToken struct {
	TokenHash string    `gorm:"size:64;primaryKey"`
	AccountID uint64    `gorm:"not null;index"`
	Purpose   string    `gorm:"size:16;not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}

type StudentProfile struct {
	StudentID   uint64    `gorm:"primaryKey" json:"student_id"`
	DisplayName string    `gorm:"size:120" json:"display_name"`
	Avatar      string    `gorm:"size:500" json:"avatar"`
	Grade       int       `json:"grade"`
	Timezone    string    `gorm:"size:80;not null;default:Asia/Shanghai" json:"timezone"`
	ParentName  string    `gorm:"size:120" json:"parent_name"`
	ParentEmail string    `gorm:"size:254" json:"parent_email"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TeacherAvailability struct {
	ID          uint64 `gorm:"primaryKey" json:"id"`
	TeacherID   uint64 `gorm:"not null;index" json:"teacher_id"`
	Weekday     int    `gorm:"not null" json:"weekday"` // 0 = Sunday
	StartMinute int    `gorm:"not null" json:"start_minute"`
	EndMinute   int    `gorm:"not null" json:"end_minute"`
	Timezone    string `gorm:"size:80;not null" json:"timezone"`
	Active      bool   `gorm:"not null;default:true" json:"active"`
}

type TeacherTimeOff struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	TeacherID uint64    `gorm:"not null;index" json:"teacher_id"`
	StartAt   time.Time `gorm:"not null;index" json:"start_at"`
	EndAt     time.Time `gorm:"not null" json:"end_at"`
	Reason    string    `gorm:"size:255" json:"reason"`
}

type Lesson struct {
	ID               uint64     `gorm:"primaryKey" json:"id"`
	StudentName      string     `gorm:"-" json:"student_name"`
	TeacherName      string     `gorm:"-" json:"teacher_name"`
	StudentID        uint64     `gorm:"not null;index:idx_lesson_student_start,priority:1" json:"student_id"`
	TeacherID        uint64     `gorm:"not null;index:idx_lesson_teacher_start,priority:1" json:"teacher_id"`
	ScheduledStartAt time.Time  `gorm:"not null;index:idx_lesson_student_start,priority:2;index:idx_lesson_teacher_start,priority:2" json:"scheduled_start_at"`
	ScheduledEndAt   time.Time  `gorm:"not null" json:"scheduled_end_at"`
	DurationMinutes  int        `gorm:"not null" json:"duration_minutes"`
	Source           string     `gorm:"size:24;not null" json:"source"`
	Status           string     `gorm:"size:24;not null;index" json:"status"`
	Note             string     `gorm:"size:500" json:"note"`
	RTCChannel       string     `gorm:"size:63;not null;uniqueIndex" json:"-"`
	TeacherRTCUID    uint32     `gorm:"not null" json:"-"`
	StudentRTCUID    uint32     `gorm:"not null" json:"-"`
	ScreenRTCUID     uint32     `gorm:"not null" json:"-"`
	WhiteboardUUID   string     `gorm:"size:80" json:"-"`
	WhiteboardStatus string     `gorm:"size:16;not null;default:pending" json:"-"`
	GraceSeconds     int        `gorm:"not null;default:60" json:"grace_seconds"`
	TeacherJoinedAt  *time.Time `json:"teacher_joined_at"`
	StudentJoinedAt  *time.Time `json:"student_joined_at"`
	ActualStartAt    *time.Time `json:"actual_start_at"`
	ActualEndAt      *time.Time `json:"actual_end_at"`
	TeachingSeconds  int        `gorm:"not null;default:0" json:"teaching_seconds"`
	CancelledAt      *time.Time `json:"cancelled_at"`
	CancelReason     string     `gorm:"size:255" json:"cancel_reason"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type LessonPresenceSegment struct {
	ID         uint64     `gorm:"primaryKey" json:"id"`
	LessonID   uint64     `gorm:"not null;index" json:"lesson_id"`
	Kind       string     `gorm:"size:16;not null" json:"kind"`
	StartedAt  time.Time  `gorm:"not null" json:"started_at"`
	LastSeenAt time.Time  `gorm:"not null" json:"last_seen_at"`
	EndedAt    *time.Time `json:"ended_at"`
}

type LessonRequest struct {
	ID         uint64 `gorm:"primaryKey"`
	ActorKind  string `gorm:"size:16;not null;uniqueIndex:idx_lesson_request,priority:1"`
	ActorID    uint64 `gorm:"not null;uniqueIndex:idx_lesson_request,priority:2"`
	RequestKey string `gorm:"size:100;not null;uniqueIndex:idx_lesson_request,priority:3"`
	LessonID   uint64 `gorm:"not null"`
}
