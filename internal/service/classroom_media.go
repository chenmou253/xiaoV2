package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"xiaov2/internal/model"
)

type ClassroomCredentials struct {
	LessonID         uint64    `json:"lesson_id"`
	ServerTime       time.Time `json:"server_time"`
	ScheduledStartAt time.Time `json:"scheduled_start_at"`
	ScheduledEndAt   time.Time `json:"scheduled_end_at"`
	GraceSeconds     int       `json:"grace_seconds"`
	RTC              struct {
		AppID       string `json:"app_id"`
		Channel     string `json:"channel"`
		UID         uint32 `json:"uid"`
		Token       string `json:"token"`
		ScreenUID   uint32 `json:"screen_uid,omitempty"`
		ScreenToken string `json:"screen_token,omitempty"`
	} `json:"rtc"`
	Whiteboard struct {
		AppIdentifier string `json:"app_identifier"`
		Region        string `json:"region"`
		UUID          string `json:"uuid"`
		RoomToken     string `json:"room_token"`
	} `json:"whiteboard"`
}

func (s *ClassroomService) prepareRoom(ctx context.Context, lesson model.Lesson) (string, error) {
	if lesson.WhiteboardStatus == "ready" && lesson.WhiteboardUUID != "" {
		return lesson.WhiteboardUUID, nil
	}
	if s.whiteboard.AppIdentifier == "" || s.whiteboard.AccessKey == "" || s.whiteboard.SecretKey == "" {
		return "", &AppError{503, 50300, "互动白板尚未配置"}
	}
	q := s.db.WithContext(ctx).Model(&model.Lesson{}).Where("id = ? AND status IN ?", lesson.ID, []string{"scheduled", "in_progress"}).Where("whiteboard_status IN ? OR (whiteboard_status = ? AND updated_at < ?)", []string{"pending", "failed"}, "preparing", time.Now().UTC().Add(-time.Minute)).Update("whiteboard_status", "preparing")
	if q.Error != nil {
		return "", q.Error
	}
	if q.RowsAffected == 0 {
		var fresh model.Lesson
		if err := s.db.WithContext(ctx).First(&fresh, lesson.ID).Error; err != nil {
			return "", err
		}
		if fresh.WhiteboardStatus == "ready" && fresh.WhiteboardUUID != "" {
			return fresh.WhiteboardUUID, nil
		}
		return "", &AppError{503, 50300, "课堂白板正在准备，请稍后重试"}
	}
	uuid, err := s.whiteboard.CreateRoom(ctx)
	if err != nil {
		_ = s.db.WithContext(ctx).Model(&model.Lesson{}).Where("id = ? AND whiteboard_status = ?", lesson.ID, "preparing").Update("whiteboard_status", "failed").Error
		return "", &AppError{503, 50300, "课堂白板暂不可用，请稍后重试"}
	}
	stored := s.db.WithContext(ctx).Model(&model.Lesson{}).Where("id = ? AND whiteboard_status = ? AND status IN ?", lesson.ID, "preparing", []string{"scheduled", "in_progress"}).Updates(map[string]any{"whiteboard_uuid": uuid, "whiteboard_status": "ready"})
	if stored.Error != nil || stored.RowsAffected == 0 {
		if cleanupErr := s.whiteboard.DisableRoom(ctx, uuid); cleanupErr != nil {
			fmt.Printf("orphan whiteboard room %s cleanup failed: %v\n", uuid, cleanupErr)
		}
		if stored.Error != nil {
			return "", stored.Error
		}
		return "", &AppError{403, 40310, "课程已经结束"}
	}
	return uuid, nil
}

func (s *ClassroomService) Join(ctx context.Context, kind string, id, lessonID uint64) (ClassroomCredentials, error) {
	var out ClassroomCredentials
	if kind != "teacher" && kind != "student" {
		return out, &AppError{403, 40300, "无权进入此课堂"}
	}
	lesson, err := s.Lesson(ctx, kind, id, lessonID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, &AppError{403, 40300, "无权进入此课堂"}
	}
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	if lesson.Status != "scheduled" && lesson.Status != "in_progress" {
		return out, &AppError{403, 40310, "课程已经结束"}
	}
	if now.Before(lesson.ScheduledStartAt.Add(-10*time.Minute)) || !now.Before(lesson.ScheduledEndAt) {
		return out, &AppError{403, 40310, "当前不在上课时间"}
	}
	if s.rtc.AppID == "" || s.rtc.Certificate == "" {
		return out, &AppError{503, 50300, "音视频服务尚未配置"}
	}
	uuid, err := s.prepareRoom(ctx, lesson)
	if err != nil {
		return out, err
	}
	var fresh model.Lesson
	if err := s.db.WithContext(ctx).First(&fresh, lesson.ID).Error; err != nil {
		return out, err
	}
	if fresh.Status != "scheduled" && fresh.Status != "in_progress" {
		return out, &AppError{403, 40310, "课程已经结束"}
	}
	if !time.Now().UTC().Before(fresh.ScheduledEndAt) {
		return out, &AppError{403, 40310, "当前不在上课时间"}
	}
	until := lesson.ScheduledEndAt.Add(time.Duration(lesson.GraceSeconds) * time.Second)
	uid := lesson.StudentRTCUID
	if kind == "teacher" {
		uid = lesson.TeacherRTCUID
	}
	token, err := s.rtc.Token(lesson.RTCChannel, uid, until)
	if err != nil {
		return out, &AppError{503, 50300, "音视频服务暂不可用"}
	}
	roomToken, err := s.whiteboard.RoomToken(ctx, uuid, until)
	if err != nil {
		return out, &AppError{503, 50300, "互动白板暂不可用"}
	}
	out.LessonID = lesson.ID
	out.ServerTime = now
	out.ScheduledStartAt = lesson.ScheduledStartAt
	out.ScheduledEndAt = lesson.ScheduledEndAt
	out.GraceSeconds = lesson.GraceSeconds
	out.RTC.AppID = s.rtc.AppID
	out.RTC.Channel = lesson.RTCChannel
	out.RTC.UID = uid
	out.RTC.Token = token
	if kind == "teacher" {
		screen, e := s.rtc.Token(lesson.RTCChannel, lesson.ScreenRTCUID, until)
		if e != nil {
			return ClassroomCredentials{}, e
		}
		out.RTC.ScreenUID = lesson.ScreenRTCUID
		out.RTC.ScreenToken = screen
	}
	out.Whiteboard.AppIdentifier = s.whiteboard.AppIdentifier
	out.Whiteboard.Region = s.whiteboard.Region
	out.Whiteboard.UUID = uuid
	out.Whiteboard.RoomToken = roomToken
	return out, nil
}

func (s *ClassroomService) PrepareRooms(ctx context.Context) error {
	if s.whiteboard.AppIdentifier == "" || s.whiteboard.AccessKey == "" || s.whiteboard.SecretKey == "" || s.whiteboard.Region == "" {
		return nil
	}
	now := time.Now().UTC()
	var rows []model.Lesson
	if err := s.db.WithContext(ctx).Where("status = ? AND scheduled_end_at > ? AND scheduled_start_at < ? AND (whiteboard_status = ? OR (whiteboard_status IN ? AND updated_at < ?))", "scheduled", now, now.Add(time.Hour), "pending", []string{"failed", "preparing"}, now.Add(-time.Minute)).Order("scheduled_start_at").Limit(5).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := s.prepareRoom(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func (s *ClassroomService) CloseRooms(ctx context.Context) error {
	var rows []model.Lesson
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Where("whiteboard_uuid <> ? AND (whiteboard_status = ? OR (whiteboard_status = ? AND updated_at < ?)) AND (status = ? OR scheduled_end_at <= ?)", "", "ready", "closing", now.Add(-time.Minute), "cancelled", now.Add(-60*time.Second)).Order("id").Limit(50).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		claim := s.db.WithContext(ctx).Model(&model.Lesson{}).Where("id = ? AND (whiteboard_status = ? OR (whiteboard_status = ? AND updated_at < ?))", row.ID, "ready", "closing", now.Add(-time.Minute)).Update("whiteboard_status", "closing")
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			continue
		}
		if err := s.whiteboard.DisableRoom(ctx, row.WhiteboardUUID); err != nil {
			_ = s.db.WithContext(ctx).Model(&model.Lesson{}).Where("id = ?", row.ID).Update("whiteboard_status", "ready").Error
			return err
		}
		if err := s.db.WithContext(ctx).Model(&model.Lesson{}).Where("id = ?", row.ID).Update("whiteboard_status", "closed").Error; err != nil {
			return err
		}
	}
	return nil
}
