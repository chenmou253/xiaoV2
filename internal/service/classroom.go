package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/model"
	"xiaov2/internal/rtc"
	"xiaov2/internal/whiteboard"
)

const lessonDuration = 30
const lessonGraceSeconds = 60

type ClassroomService struct {
	db         *gorm.DB
	platform   *PlatformService
	rtc        rtc.Agora
	whiteboard whiteboard.Agora
}

func classroomAudit(tx *gorm.DB, actor uint64, action string, target uint64, detail any) error {
	if actor == 0 {
		return nil
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return tx.Create(&model.AuditLog{ActorID: actor, Action: action, Target: fmt.Sprint(target), Detail: string(raw)}).Error
}

func duplicateKey(err error) bool {
	var sqlErr *mysql.MySQLError
	return errors.As(err, &sqlErr) && sqlErr.Number == 1062
}

func NewClassroomService(db *gorm.DB, platform *PlatformService) *ClassroomService {
	cfg := platform.cfg
	return &ClassroomService{db: db, platform: platform, rtc: rtc.Agora{AppID: cfg.AgoraAppID, Certificate: cfg.AgoraAppCertificate}, whiteboard: whiteboard.Agora{AppIdentifier: cfg.WhiteboardAppIdentifier, AccessKey: cfg.WhiteboardAccessKey, SecretKey: cfg.WhiteboardSecretKey, Region: cfg.WhiteboardRegion}}
}

type TeacherInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
	Country     string `json:"country"`
	Timezone    string `json:"timezone"`
	Bio         string `json:"bio"`
	Active      *bool  `json:"active"`
}

func validTimezone(name string) bool {
	if name == "" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

func (s *ClassroomService) CreateTeacher(ctx context.Context, in TeacherInput, actor uint64) (model.Teacher, error) {
	in.Email = normalizeEmail(in.Email)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.Email == "" || in.DisplayName == "" || !validTimezone(in.Timezone) {
		return model.Teacher{}, bad("请填写有效的邮箱、姓名和时区")
	}
	if len(in.DisplayName) > 120 || len(in.Country) > 80 || len(in.Bio) > 4000 {
		return model.Teacher{}, bad("外教资料过长")
	}
	raw, err := randomToken()
	if err != nil {
		return model.Teacher{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), 12)
	if err != nil {
		return model.Teacher{}, err
	}
	teacher := model.Teacher{Email: in.Email, PasswordHash: string(hash), DisplayName: in.DisplayName, Avatar: in.Avatar, Country: in.Country, Timezone: in.Timezone, Bio: in.Bio, Verified: false, Active: true}
	if err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&teacher).Error; e != nil {
			return e
		}
		return classroomAudit(tx, actor, "teacher.create", teacher.ID, map[string]any{"email": teacher.Email, "timezone": teacher.Timezone})
	}); err != nil {
		if duplicateKey(err) {
			return model.Teacher{}, conflict("外教邮箱已存在")
		}
		return model.Teacher{}, err
	}
	if _, err = s.platform.issueToken(ctx, "teacher", "reset", teacher.ID, teacher.Email); err != nil {
		fmt.Printf("teacher activation email failed for id %d: %v\n", teacher.ID, err)
		sent := false
		teacher.ActivationEmailSent = &sent
		return teacher, nil
	}
	sent := true
	teacher.ActivationEmailSent = &sent
	return teacher, nil
}

func (s *ClassroomService) Teachers(ctx context.Context, public bool) ([]model.Teacher, error) {
	q := s.db.WithContext(ctx).Model(&model.Teacher{}).Order("id DESC")
	if public {
		q = q.Where("active = ? AND verified = ?", true, true)
	}
	rows := make([]model.Teacher, 0)
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *ClassroomService) Teacher(ctx context.Context, id uint64) (model.Teacher, error) {
	var row model.Teacher
	err := s.db.WithContext(ctx).First(&row, id).Error
	return row, err
}

func (s *ClassroomService) UpdateTeacher(ctx context.Context, id uint64, in TeacherInput, actor uint64) (model.Teacher, error) {
	if in.Timezone != "" && !validTimezone(in.Timezone) {
		return model.Teacher{}, bad("时区无效")
	}
	if len(in.DisplayName) > 120 || len(in.Country) > 80 || len(in.Bio) > 4000 || len(in.Avatar) > 500 || (in.DisplayName != "" && strings.TrimSpace(in.DisplayName) == "") {
		return model.Teacher{}, bad("外教资料无效或过长")
	}
	updates := map[string]any{}
	if in.DisplayName != "" {
		updates["display_name"] = strings.TrimSpace(in.DisplayName)
	}
	if in.Country != "" {
		updates["country"] = in.Country
	}
	if in.Timezone != "" {
		updates["timezone"] = in.Timezone
	}
	if in.Bio != "" {
		updates["bio"] = in.Bio
	}
	if in.Avatar != "" {
		updates["avatar"] = in.Avatar
	}
	if in.Active != nil {
		updates["active"] = *in.Active
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.Teacher
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return err
		}
		if in.Timezone != "" && in.Timezone != row.Timezone {
			if err := tx.Model(&model.TeacherAvailability{}).Where("teacher_id = ?", id).Update("timezone", in.Timezone).Error; err != nil {
				return err
			}
		}
		if in.Active != nil && !*in.Active {
			if err := tx.Where("account_id = ?", id).Delete(&model.TeacherSession{}).Error; err != nil {
				return err
			}
		}
		return classroomAudit(tx, actor, "teacher.update", id, updates)
	})
	if err != nil {
		return model.Teacher{}, err
	}
	return s.Teacher(ctx, id)
}

func (s *ClassroomService) Availability(ctx context.Context, teacherID uint64) ([]model.TeacherAvailability, error) {
	rows := make([]model.TeacherAvailability, 0)
	if err := s.db.WithContext(ctx).Where("teacher_id = ?", teacherID).Order("weekday, start_minute").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *ClassroomService) ReplaceAvailability(ctx context.Context, teacherID uint64, rows []model.TeacherAvailability, actor uint64) error {
	if len(rows) > 40 {
		return bad("开放时间过多")
	}
	for i := range rows {
		v := &rows[i]
		if v.Weekday < 0 || v.Weekday > 6 || v.StartMinute < 0 || v.EndMinute > 1440 || v.StartMinute >= v.EndMinute || v.StartMinute%30 != 0 || v.EndMinute%30 != 0 {
			return bad("开放时间必须是同一天内的半小时区间")
		}
		for j := 0; j < i; j++ {
			if rows[j].Weekday == v.Weekday && rows[j].StartMinute < v.EndMinute && rows[j].EndMinute > v.StartMinute {
				return conflict("开放时间重叠")
			}
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var teacher model.Teacher
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&teacher, teacherID).Error; err != nil {
			return err
		}
		if err := tx.Where("teacher_id = ?", teacherID).Delete(&model.TeacherAvailability{}).Error; err != nil {
			return err
		}
		for i := range rows {
			rows[i].ID = 0
			rows[i].TeacherID = teacherID
			rows[i].Timezone = teacher.Timezone
			rows[i].Active = true
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		return classroomAudit(tx, actor, "teacher.availability", teacherID, rows)
	})
}

func (s *ClassroomService) TimeOff(ctx context.Context, teacherID uint64) ([]model.TeacherTimeOff, error) {
	rows := make([]model.TeacherTimeOff, 0)
	if err := s.db.WithContext(ctx).Where("teacher_id = ? AND end_at > ?", teacherID, time.Now().UTC()).Order("start_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *ClassroomService) AddTimeOff(ctx context.Context, teacherID uint64, start, end time.Time, reason string, actor uint64) (model.TeacherTimeOff, error) {
	if !end.After(start) || end.Sub(start) > 30*24*time.Hour || len(reason) > 255 {
		return model.TeacherTimeOff{}, bad("请假时间无效")
	}
	row := model.TeacherTimeOff{TeacherID: teacherID, StartAt: start.UTC(), EndAt: end.UTC(), Reason: reason}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var teacher model.Teacher
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&teacher, teacherID).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.Lesson{}).Where("teacher_id = ? AND status IN ? AND scheduled_start_at < ? AND scheduled_end_at > ?", teacherID, []string{"scheduled", "in_progress"}, end, start).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return conflict("请假时间内已有课程，请先处理课程")
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return classroomAudit(tx, actor, "teacher.time-off", teacherID, row)
	})
	return row, err
}

func (s *ClassroomService) StudentProfile(ctx context.Context, studentID uint64) (model.StudentProfile, error) {
	var p model.StudentProfile
	err := s.db.WithContext(ctx).First(&p, "student_id = ?", studentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.StudentProfile{StudentID: studentID, Timezone: "Asia/Shanghai"}, nil
	}
	return p, err
}

func (s *ClassroomService) SaveStudentProfile(ctx context.Context, studentID uint64, p model.StudentProfile) (model.StudentProfile, error) {
	if !validTimezone(p.Timezone) || p.Grade < 0 || p.Grade > 12 || len(p.DisplayName) > 120 || len(p.Avatar) > 500 || len(p.ParentName) > 120 || len(p.ParentEmail) > 254 {
		return model.StudentProfile{}, bad("学生资料无效")
	}
	if p.ParentEmail != "" {
		if _, err := mail.ParseAddress(p.ParentEmail); err != nil {
			return model.StudentProfile{}, bad("家长邮箱无效")
		}
	}
	p.StudentID = studentID
	err := s.db.WithContext(ctx).Save(&p).Error
	return p, err
}

type Slot struct {
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Available bool      `json:"available"`
}

func withinAvailability(start, end time.Time, rows []model.TeacherAvailability) bool {
	for _, a := range rows {
		if !a.Active {
			continue
		}
		loc, err := time.LoadLocation(a.Timezone)
		if err != nil {
			continue
		}
		localStart, localEnd := start.In(loc), end.In(loc)
		if int(localStart.Weekday()) != a.Weekday {
			continue
		}
		minute := localStart.Hour()*60 + localStart.Minute()
		endMinute := localEnd.Hour()*60 + localEnd.Minute()
		if localStart.YearDay() != localEnd.YearDay() || localStart.Year() != localEnd.Year() {
			if localEnd.Hour() != 0 || localEnd.Minute() != 0 || localEnd.Second() != 0 {
				continue
			}
			endMinute = 1440
		}
		if minute >= a.StartMinute && endMinute <= a.EndMinute && minute%30 == 0 {
			return true
		}
	}
	return false
}

func overlaps(start, end time.Time, lessons []model.Lesson) bool {
	for _, l := range lessons {
		if start.Before(l.ScheduledEndAt) && end.After(l.ScheduledStartAt) {
			return true
		}
	}
	return false
}

func blocked(start, end time.Time, off []model.TeacherTimeOff) bool {
	for _, o := range off {
		if start.Before(o.EndAt) && end.After(o.StartAt) {
			return true
		}
	}
	return false
}

func studentDayBounds(start time.Time, timezone string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	local := start.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return day.UTC(), day.AddDate(0, 0, 1).UTC(), nil
}

func (s *ClassroomService) Slots(ctx context.Context, teacherID, studentID uint64, day string) ([]Slot, error) {
	teacher, err := s.Teacher(ctx, teacherID)
	if err != nil {
		return nil, err
	}
	if !teacher.Active || !teacher.Verified {
		return nil, bad("外教暂不可预约")
	}
	p, err := s.StudentProfile(ctx, studentID)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return nil, bad("学生时区无效")
	}
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return nil, bad("日期格式无效")
	}
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).UTC()
	end := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc).UTC()
	if start.After(time.Now().UTC().AddDate(0, 0, 15)) || end.Before(time.Now().UTC()) {
		return []Slot{}, nil
	}
	avail, err := s.Availability(ctx, teacherID)
	if err != nil {
		return nil, err
	}
	var off []model.TeacherTimeOff
	if err = s.db.WithContext(ctx).Where("teacher_id = ? AND start_at < ? AND end_at > ?", teacherID, end.Add(lessonDuration*time.Minute), start).Find(&off).Error; err != nil {
		return nil, err
	}
	var lessons []model.Lesson
	if err = s.db.WithContext(ctx).Where("status IN ? AND scheduled_start_at < ? AND scheduled_end_at > ? AND (teacher_id = ? OR student_id = ?)", []string{"scheduled", "in_progress"}, end.Add(lessonDuration*time.Minute), start, teacherID, studentID).Find(&lessons).Error; err != nil {
		return nil, err
	}
	daily := 0
	for _, l := range lessons {
		if l.StudentID == studentID && !l.ScheduledStartAt.Before(start) && l.ScheduledStartAt.Before(end) {
			daily++
		}
	}
	now := time.Now().UTC()
	slots := make([]Slot, 0)
	for t := start; t.Before(end); t = t.Add(15 * time.Minute) {
		to := t.Add(lessonDuration * time.Minute)
		if !withinAvailability(t, to, avail) {
			continue
		}
		available := !t.Before(now.Add(30*time.Minute)) && !t.After(now.AddDate(0, 0, 14)) && daily < 3 && !blocked(t, to, off) && !overlaps(t, to, lessons)
		slots = append(slots, Slot{t, to, available})
	}
	return slots, nil
}

type BookLessonInput struct {
	StudentID       uint64    `json:"student_id"`
	TeacherID       uint64    `json:"teacher_id"`
	StartAt         time.Time `json:"start_at"`
	DurationMinutes int       `json:"duration_minutes"`
	Note            string    `json:"note"`
	IdempotencyKey  string    `json:"idempotency_key"`
}

func activeOverlap(tx *gorm.DB, column string, id uint64, start, end time.Time) (bool, error) {
	var n int64
	err := tx.Model(&model.Lesson{}).Where(column+" = ? AND status IN ? AND scheduled_start_at < ? AND scheduled_end_at > ?", id, []string{"scheduled", "in_progress"}, end, start).Count(&n).Error
	return n > 0, err
}

func (s *ClassroomService) BookLesson(ctx context.Context, actorKind string, actorID uint64, in BookLessonInput) (model.Lesson, error) {
	if in.DurationMinutes == 0 {
		in.DurationMinutes = lessonDuration
	}
	if in.TeacherID == 0 || in.StudentID == 0 || in.DurationMinutes != lessonDuration || in.StartAt.IsZero() || len(in.Note) > 500 || len(in.IdempotencyKey) < 8 || len(in.IdempotencyKey) > 100 {
		return model.Lesson{}, bad("预约信息无效")
	}
	if actorKind != "student" && actorKind != "admin" {
		return model.Lesson{}, bad("账号类型无效")
	}
	if actorKind == "student" && in.StudentID != actorID {
		return model.Lesson{}, bad("只能为自己预约")
	}
	start := in.StartAt.UTC().Truncate(time.Minute)
	if !start.Equal(in.StartAt.UTC()) {
		return model.Lesson{}, bad("开始时间必须精确到分钟")
	}
	end := start.Add(time.Duration(in.DurationMinutes) * time.Minute)
	var result model.Lesson
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var teacher model.Teacher
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&teacher, in.TeacherID).Error; e != nil {
				return e
			}
			if !teacher.Active || !teacher.Verified {
				return bad("外教暂不可预约")
			}
			teacherZone, e := time.LoadLocation(teacher.Timezone)
			if e != nil || start.In(teacherZone).Minute()%30 != 0 {
				return bad("课程开始时间须对齐外教当地的半小时刻度")
			}
			var student model.Student
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&student, in.StudentID).Error; e != nil {
				return e
			}
			if !student.Active || !student.Verified {
				return bad("学生账号不可用")
			}
			var request model.LessonRequest
			e = tx.Where("actor_kind = ? AND actor_id = ? AND request_key = ?", actorKind, actorID, in.IdempotencyKey).First(&request).Error
			if e == nil {
				if e = tx.First(&result, request.LessonID).Error; e != nil {
					return e
				}
				if result.StudentID != in.StudentID || result.TeacherID != in.TeacherID || !result.ScheduledStartAt.Equal(start) {
					return conflict("幂等键已用于其他课程")
				}
				return nil
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			now := time.Now().UTC()
			if !start.After(now) {
				return bad("只能预约未来课程")
			}
			if actorKind == "student" {
				if start.Before(now.Add(30*time.Minute)) || start.After(now.AddDate(0, 0, 14)) {
					return bad("预约时间不在允许范围内")
				}
				var avail []model.TeacherAvailability
				if e := tx.Where("teacher_id = ? AND active = ?", in.TeacherID, true).Find(&avail).Error; e != nil {
					return e
				}
				if !withinAvailability(start, end, avail) {
					return conflict("此时段未开放")
				}
				p, e := s.StudentProfile(ctx, in.StudentID)
				if e != nil {
					return e
				}
				dayStart, dayEnd, e := studentDayBounds(start, p.Timezone)
				if e != nil {
					return e
				}
				var count int64
				if e := tx.Model(&model.Lesson{}).Where("student_id = ? AND status IN ? AND scheduled_start_at >= ? AND scheduled_start_at < ?", in.StudentID, []string{"scheduled", "in_progress"}, dayStart, dayEnd).Count(&count).Error; e != nil {
					return e
				}
				if count >= 3 {
					return conflict("当天预约课程已达上限")
				}
			}
			var offCount int64
			if e := tx.Model(&model.TeacherTimeOff{}).Where("teacher_id = ? AND start_at < ? AND end_at > ?", in.TeacherID, end, start).Count(&offCount).Error; e != nil {
				return e
			}
			if offCount > 0 {
				return conflict("外教该时段请假")
			}
			for _, participant := range []struct {
				column string
				id     uint64
			}{{"teacher_id", in.TeacherID}, {"student_id", in.StudentID}} {
				busy, e := activeOverlap(tx, participant.column, participant.id, start, end)
				if e != nil {
					return e
				}
				if busy {
					return conflict("时段已被预约")
				}
			}
			bytes := make([]byte, 12)
			if _, e := rand.Read(bytes); e != nil {
				return e
			}
			result = model.Lesson{StudentID: in.StudentID, TeacherID: in.TeacherID, ScheduledStartAt: start, ScheduledEndAt: end, DurationMinutes: in.DurationMinutes, Source: actorKind, Status: "scheduled", Note: in.Note, RTCChannel: "lesson_" + hex.EncodeToString(bytes), TeacherRTCUID: 1, StudentRTCUID: 2, ScreenRTCUID: 3, WhiteboardStatus: "pending", GraceSeconds: lessonGraceSeconds}
			if e := tx.Create(&result).Error; e != nil {
				return e
			}
			if e := tx.Create(&model.LessonRequest{ActorKind: actorKind, ActorID: actorID, RequestKey: in.IdempotencyKey, LessonID: result.ID}).Error; e != nil {
				return e
			}
			if actorKind == "admin" {
				return classroomAudit(tx, actorID, "lesson.create", result.ID, map[string]any{"teacher_id": in.TeacherID, "student_id": in.StudentID, "start_at": start, "bypass_availability": true})
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err == nil {
			return result, nil
		}
		var app *AppError
		if errors.As(err, &app) || errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Lesson{}, err
		}
		if duplicateKey(err) {
			var request model.LessonRequest
			if e := s.db.WithContext(ctx).Where("actor_kind = ? AND actor_id = ? AND request_key = ?", actorKind, actorID, in.IdempotencyKey).First(&request).Error; e == nil {
				if e = s.db.WithContext(ctx).First(&result, request.LessonID).Error; e == nil && result.StudentID == in.StudentID && result.TeacherID == in.TeacherID && result.ScheduledStartAt.Equal(start) {
					return result, nil
				}
			}
			return model.Lesson{}, conflict("幂等键已用于其他课程")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "deadlock") && !strings.Contains(strings.ToLower(err.Error()), "lock wait timeout") {
			break
		}
	}
	return model.Lesson{}, err
}

func (s *ClassroomService) Lessons(ctx context.Context, kind string, id uint64) ([]model.Lesson, error) {
	rows := make([]model.Lesson, 0)
	q := s.db.WithContext(ctx).Order("scheduled_start_at DESC").Limit(200)
	if kind == "student" {
		q = q.Where("student_id = ?", id)
	} else if kind == "teacher" {
		q = q.Where("teacher_id = ?", id)
	} else if kind != "admin" {
		return nil, bad("账号类型无效")
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	s.decorateLessons(ctx, rows)
	return rows, nil
}

func (s *ClassroomService) Lesson(ctx context.Context, kind string, id, lessonID uint64) (model.Lesson, error) {
	var row model.Lesson
	q := s.db.WithContext(ctx)
	if kind == "student" {
		q = q.Where("student_id = ?", id)
	} else if kind == "teacher" {
		q = q.Where("teacher_id = ?", id)
	} else if kind != "admin" {
		return row, bad("账号类型无效")
	}
	if err := q.First(&row, lessonID).Error; err != nil {
		return row, err
	}
	rows := []model.Lesson{row}
	s.decorateLessons(ctx, rows)
	return rows[0], nil
}

func (s *ClassroomService) decorateLessons(ctx context.Context, rows []model.Lesson) {
	if len(rows) == 0 {
		return
	}
	studentIDs, teacherIDs := make([]uint64, 0, len(rows)), make([]uint64, 0, len(rows))
	for _, row := range rows {
		studentIDs = append(studentIDs, row.StudentID)
		teacherIDs = append(teacherIDs, row.TeacherID)
	}
	var profiles []model.StudentProfile
	var students []model.Student
	var teachers []model.Teacher
	_ = s.db.WithContext(ctx).Where("id IN ?", studentIDs).Find(&students).Error
	_ = s.db.WithContext(ctx).Where("student_id IN ?", studentIDs).Find(&profiles).Error
	_ = s.db.WithContext(ctx).Where("id IN ?", teacherIDs).Find(&teachers).Error
	names := map[uint64]string{}
	teacherNames := map[uint64]string{}
	for _, student := range students {
		names[student.ID] = student.Email
	}
	for _, p := range profiles {
		if p.DisplayName != "" {
			names[p.StudentID] = p.DisplayName
		}
	}
	for _, t := range teachers {
		teacherNames[t.ID] = t.DisplayName
	}
	for i := range rows {
		rows[i].StudentName = names[rows[i].StudentID]
		rows[i].TeacherName = teacherNames[rows[i].TeacherID]
	}
}

func (s *ClassroomService) CancelLesson(ctx context.Context, lessonID uint64, reason string, actor uint64) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 255 {
		return bad("请填写取消原因")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.Lesson
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, lessonID).Error; err != nil {
			return err
		}
		if row.Status != "scheduled" && row.Status != "in_progress" {
			return conflict("课程已结束")
		}
		now := time.Now().UTC()
		if err := tx.Model(&row).Updates(map[string]any{"status": "cancelled", "cancelled_at": now, "cancel_reason": reason}).Error; err != nil {
			return err
		}
		return classroomAudit(tx, actor, "lesson.cancel", lessonID, map[string]any{"reason": reason, "previous_status": row.Status})
	})
	if err != nil {
		return err
	}
	if err = s.CloseRooms(ctx); err != nil {
		fmt.Printf("whiteboard close after cancellation: %v\n", err)
	}
	return nil
}

type MonthStats struct {
	CompletedLessons int64 `json:"completed_lessons"`
	TeachingSeconds  int64 `json:"teaching_seconds"`
	StudentNoShow    int64 `json:"student_no_show"`
	TeacherNoShow    int64 `json:"teacher_no_show"`
	Cancelled        int64 `json:"cancelled"`
}

func (s *ClassroomService) Statistics(ctx context.Context, kind string, id uint64, month string) (MonthStats, error) {
	if len(month) != 7 {
		return MonthStats{}, bad("月份格式无效")
	}
	d, err := time.Parse("2006-01", month)
	if err != nil {
		return MonthStats{}, bad("月份格式无效")
	}
	zone := "Asia/Shanghai"
	if kind == "teacher" {
		t, e := s.Teacher(ctx, id)
		if e != nil {
			return MonthStats{}, e
		}
		zone = t.Timezone
	} else if kind == "student" {
		p, e := s.StudentProfile(ctx, id)
		if e != nil {
			return MonthStats{}, e
		}
		zone = p.Timezone
	} else {
		return MonthStats{}, bad("账号类型无效")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return MonthStats{}, bad("资料中的时区无效，请联系管理员修改")
	}
	start := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, loc).UTC()
	end := time.Date(d.Year(), d.Month()+1, 1, 0, 0, 0, 0, loc).UTC()
	column := "teacher_id"
	if kind == "student" {
		column = "student_id"
	}
	var rows []model.Lesson
	if err = s.db.WithContext(ctx).Where(column+" = ? AND scheduled_start_at >= ? AND scheduled_start_at < ?", id, start, end).Find(&rows).Error; err != nil {
		return MonthStats{}, err
	}
	var out MonthStats
	for _, l := range rows {
		switch l.Status {
		case "completed":
			out.CompletedLessons++
			out.TeachingSeconds += int64(l.TeachingSeconds)
		case "student_no_show":
			out.StudentNoShow++
		case "teacher_no_show":
			out.TeacherNoShow++
		case "cancelled":
			out.Cancelled++
		}
	}
	return out, nil
}

func (s *ClassroomService) StudentDashboard(ctx context.Context, id uint64) (map[string]any, error) {
	p, err := s.StudentProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	var next model.Lesson
	err = s.db.WithContext(ctx).Where("student_id = ? AND status IN ? AND scheduled_end_at > ?", id, []string{"scheduled", "in_progress"}, time.Now().UTC()).Order("scheduled_start_at").First(&next).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	month := time.Now().Format("2006-01")
	if loc, e := time.LoadLocation(p.Timezone); e == nil {
		month = time.Now().In(loc).Format("2006-01")
	}
	stats, err := s.Statistics(ctx, "student", id, month)
	if err != nil {
		return nil, err
	}
	var lesson any
	if next.ID != 0 {
		rows := []model.Lesson{next}
		s.decorateLessons(ctx, rows)
		next = rows[0]
		lesson = next
	}
	return map[string]any{"profile": p, "next_lesson": lesson, "month_stats": stats}, nil
}

func (s *ClassroomService) Presence(ctx context.Context, kind string, id, lessonID uint64, action string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lesson model.Lesson
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lesson, lessonID).Error; err != nil {
			return err
		}
		if (kind == "student" && lesson.StudentID != id) || (kind == "teacher" && lesson.TeacherID != id) {
			return &AppError{403, 40300, "无权进入此课堂"}
		}
		now := time.Now().UTC()
		var segment model.LessonPresenceSegment
		err := tx.Where("lesson_id = ? AND kind = ? AND ended_at IS NULL", lessonID, kind).Order("id DESC").First(&segment).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if action == "leave" {
			if segment.ID != 0 {
				return tx.Model(&segment).Updates(map[string]any{"ended_at": now, "last_seen_at": now}).Error
			}
			return nil
		}
		if lesson.Status != "scheduled" && lesson.Status != "in_progress" {
			return &AppError{403, 40310, "课程已经结束"}
		}
		if now.Before(lesson.ScheduledStartAt.Add(-10*time.Minute)) || !now.Before(lesson.ScheduledEndAt) {
			return &AppError{403, 40310, "当前不在上课时间"}
		}
		if action != "connected" && action != "heartbeat" {
			return bad("出勤事件无效")
		}
		if segment.ID == 0 || now.Sub(segment.LastSeenAt) > 60*time.Second {
			if segment.ID != 0 {
				if err := tx.Model(&segment).Update("ended_at", segment.LastSeenAt.Add(60*time.Second)).Error; err != nil {
					return err
				}
			}
			segment = model.LessonPresenceSegment{LessonID: lessonID, Kind: kind, StartedAt: now, LastSeenAt: now}
			if err := tx.Create(&segment).Error; err != nil {
				return err
			}
		} else if err := tx.Model(&segment).Update("last_seen_at", now).Error; err != nil {
			return err
		}
		field := "student_joined_at"
		otherKind := "teacher"
		if kind == "teacher" {
			field, otherKind = "teacher_joined_at", "student"
		}
		if (kind == "teacher" && lesson.TeacherJoinedAt == nil) || (kind == "student" && lesson.StudentJoinedAt == nil) {
			if err := tx.Model(&lesson).Update(field, now).Error; err != nil {
				return err
			}
		}
		var otherOnline int64
		if err := tx.Model(&model.LessonPresenceSegment{}).Where("lesson_id = ? AND kind = ? AND ended_at IS NULL AND last_seen_at >= ?", lessonID, otherKind, now.Add(-60*time.Second)).Count(&otherOnline).Error; err != nil {
			return err
		}
		if otherOnline > 0 && !now.Before(lesson.ScheduledStartAt) && lesson.ActualStartAt == nil {
			if err := tx.Model(&lesson).Updates(map[string]any{"status": "in_progress", "actual_start_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *ClassroomService) Reconcile(ctx context.Context) error {
	var lessons []model.Lesson
	if err := s.db.WithContext(ctx).Where("status IN ? AND scheduled_end_at <= ?", []string{"scheduled", "in_progress"}, time.Now().UTC()).Order("scheduled_end_at").Limit(100).Find(&lessons).Error; err != nil {
		return err
	}
	for _, candidate := range lessons {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var l model.Lesson
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&l, candidate.ID).Error; err != nil {
				return err
			}
			if l.Status != "scheduled" && l.Status != "in_progress" {
				return nil
			}
			var segments []model.LessonPresenceSegment
			if err := tx.Where("lesson_id = ?", l.ID).Order("started_at").Find(&segments).Error; err != nil {
				return err
			}
			var teachers, students []model.LessonPresenceSegment
			for _, v := range segments {
				if v.Kind == "teacher" {
					teachers = append(teachers, v)
				} else {
					students = append(students, v)
				}
			}
			clip := func(v model.LessonPresenceSegment) (time.Time, time.Time) {
				a := v.StartedAt
				if a.Before(l.ScheduledStartAt) {
					a = l.ScheduledStartAt
				}
				b := v.LastSeenAt.Add(20 * time.Second)
				if v.EndedAt != nil && v.EndedAt.Before(b) {
					b = *v.EndedAt
				}
				if b.After(l.ScheduledEndAt) {
					b = l.ScheduledEndAt
				}
				return a, b
			}
			present := func(rows []model.LessonPresenceSegment) bool {
				for _, v := range rows {
					a, b := clip(v)
					if b.After(a) {
						return true
					}
				}
				return false
			}
			seconds := 0
			var first, last *time.Time
			for _, t := range teachers {
				ta, tb := clip(t)
				for _, u := range students {
					ua, ub := clip(u)
					a := ta
					if ua.After(a) {
						a = ua
					}
					b := tb
					if ub.Before(b) {
						b = ub
					}
					if b.After(a) {
						seconds += int(b.Sub(a).Seconds())
						if first == nil || a.Before(*first) {
							x := a
							first = &x
						}
						if last == nil || b.After(*last) {
							x := b
							last = &x
						}
					}
				}
			}
			status := "expired"
			if first != nil {
				status = "completed"
			} else if present(teachers) {
				status = "student_no_show"
			} else if present(students) {
				status = "teacher_no_show"
			}
			return tx.Model(&l).Updates(map[string]any{"status": status, "actual_start_at": first, "actual_end_at": last, "teaching_seconds": seconds}).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *ClassroomService) RunReconciler(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.PrepareRooms(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("whiteboard prepare: %v\n", err)
		}
		if err := s.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("lesson reconcile: %v\n", err)
		}
		if err := s.CloseRooms(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("whiteboard close: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
