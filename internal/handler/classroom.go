package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"xiaov2/internal/model"
	"xiaov2/internal/service"
)

type ClassroomHandler struct {
	service    *service.ClassroomService
	avatarRoot string
}

func NewClassroomHandler(s *service.ClassroomService, storageRoot string) *ClassroomHandler {
	return &ClassroomHandler{service: s, avatarRoot: filepath.Join(storageRoot, "teacher-avatars")}
}

func (h *ClassroomHandler) TeacherAvatar(c *gin.Context) {
	name := c.Param("name")
	if name == "" || filepath.Base(name) != name {
		c.Status(http.StatusNotFound)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".jpg" && ext != ".png" && ext != ".gif" {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.File(filepath.Join(h.avatarRoot, name))
}

func (h *ClassroomHandler) UploadTeacherAvatar(c *gin.Context) {
	const maxSize = 5 << 20
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSize)
	if err := c.Request.ParseMultipartForm(maxSize); err != nil {
		failure(c, 400, 40000, "头像文件不能超过 5MB")
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		failure(c, 400, 40000, "请选择头像图片")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil || len(data) == 0 || len(data) > maxSize {
		failure(c, 400, 40000, "头像文件无效或超过 5MB")
		return
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 8000 || config.Height > 8000 {
		failure(c, 400, 40000, "仅支持有效的 JPG、PNG 或 GIF 图片（最大 8000×8000）")
		return
	}
	extensions := map[string]string{"jpeg": ".jpg", "png": ".png", "gif": ".gif"}
	ext, ok := extensions[format]
	if !ok {
		failure(c, 400, 40000, "仅支持 JPG、PNG 或 GIF 图片")
		return
	}
	if err = os.MkdirAll(h.avatarRoot, 0750); err != nil {
		writePlatformError(c, err)
		return
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		writePlatformError(c, err)
		return
	}
	name := hex.EncodeToString(token[:]) + ext
	path := filepath.Join(h.avatarRoot, name)
	if err = os.WriteFile(path, data, 0640); err != nil {
		writePlatformError(c, err)
		return
	}
	_, id, ok := actor(c)
	if !ok {
		_ = os.Remove(path)
		return
	}
	teacherID, ok := uintParam(c, "id")
	if !ok {
		_ = os.Remove(path)
		return
	}
	old, err := h.service.Teacher(c.Request.Context(), teacherID)
	if err != nil {
		_ = os.Remove(path)
		writePlatformError(c, err)
		return
	}
	avatarURL := "/uploads/teacher-avatars/" + name
	updated, err := h.service.UpdateTeacher(c.Request.Context(), teacherID, service.TeacherInput{Avatar: avatarURL}, id)
	if err != nil {
		_ = os.Remove(path)
		writePlatformError(c, err)
		return
	}
	if strings.HasPrefix(old.Avatar, "/uploads/teacher-avatars/") {
		oldName := strings.TrimPrefix(old.Avatar, "/uploads/teacher-avatars/")
		if oldName != name && filepath.Base(oldName) == oldName {
			_ = os.Remove(filepath.Join(h.avatarRoot, oldName))
		}
	}
	success(c, updated)
}

func actor(c *gin.Context) (string, uint64, bool) {
	u := identity(c)
	if u == nil {
		failure(c, 401, 40100, "请先登录")
		return "", 0, false
	}
	return u.Kind, u.ID, true
}

func (h *ClassroomHandler) Teachers(c *gin.Context) {
	kind, _, ok := actor(c)
	if !ok {
		return
	}
	rows, e := h.service.Teachers(c.Request.Context(), kind != "admin")
	if e != nil {
		writePlatformError(c, e)
		return
	}
	if kind != "admin" {
		publicRows := make([]teacherPublicProfile, 0, len(rows))
		for _, row := range rows {
			publicRows = append(publicRows, teacherPublicProfile{ID: row.ID, DisplayName: row.DisplayName, Avatar: row.Avatar, Country: row.Country, Bio: row.Bio, LessonDurationMinutes: row.LessonDurationMinutes})
		}
		success(c, publicRows)
		return
	}
	success(c, rows)
}

type teacherPublicProfile struct {
	ID                    uint64 `json:"id"`
	DisplayName           string `json:"display_name"`
	Avatar                string `json:"avatar"`
	Country               string `json:"country"`
	Bio                   string `json:"bio"`
	LessonDurationMinutes int    `json:"lesson_duration_minutes"`
}

func (h *ClassroomHandler) CreateTeacher(c *gin.Context) {
	var in service.TeacherInput
	if !bindJSON(c, &in) {
		return
	}
	_, actorID, ok := actor(c)
	if !ok {
		return
	}
	row, e := h.service.CreateTeacher(c.Request.Context(), in, actorID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) UpdateTeacher(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in service.TeacherInput
	if !bindJSON(c, &in) {
		return
	}
	_, actorID, ok := actor(c)
	if !ok {
		return
	}
	row, e := h.service.UpdateTeacher(c.Request.Context(), id, in, actorID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) Availability(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	rows, e := h.service.Availability(c.Request.Context(), id)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, rows)
}
func (h *ClassroomHandler) ScheduleConflicts(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	breakMinutes := 0
	if raw := c.Query("break_minutes"); raw != "" {
		var err error
		breakMinutes, err = strconv.Atoi(raw)
		if err != nil {
			failure(c, 400, 40000, "课间休息分钟数无效")
			return
		}
	}
	rows, err := h.service.TeacherScheduleConflicts(c.Request.Context(), id, breakMinutes)
	if err != nil {
		writePlatformError(c, err)
		return
	}
	success(c, rows)
}
func (h *ClassroomHandler) ReplaceAvailability(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Rows []model.TeacherAvailability `json:"rows"`
	}
	if !bindJSON(c, &in) {
		return
	}
	_, actorID, ok := actor(c)
	if !ok {
		return
	}
	e := h.service.ReplaceAvailability(c.Request.Context(), id, in.Rows, actorID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *ClassroomHandler) TimeOff(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	rows, e := h.service.TimeOff(c.Request.Context(), id)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, rows)
}
func (h *ClassroomHandler) AddTimeOff(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		StartAt time.Time `json:"start_at"`
		EndAt   time.Time `json:"end_at"`
		Reason  string    `json:"reason"`
	}
	if !bindJSON(c, &in) {
		return
	}
	_, actorID, ok := actor(c)
	if !ok {
		return
	}
	row, e := h.service.AddTimeOff(c.Request.Context(), id, in.StartAt, in.EndAt, in.Reason, actorID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) Slots(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	_, studentID, ok := actor(c)
	if !ok {
		return
	}
	rows, e := h.service.Slots(c.Request.Context(), id, studentID, c.Query("date"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"teacher_id": id, "slots": rows})
}
func (h *ClassroomHandler) Book(c *gin.Context) {
	kind, id, ok := actor(c)
	if !ok {
		return
	}
	var in service.BookLessonInput
	if !bindJSON(c, &in) {
		return
	}
	if kind == "student" {
		in.StudentID = id
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = c.GetHeader("Idempotency-Key")
	}
	row, e := h.service.BookLesson(c.Request.Context(), kind, id, in)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) Lessons(c *gin.Context) {
	kind, id, ok := actor(c)
	if !ok {
		return
	}
	rows, e := h.service.Lessons(c.Request.Context(), kind, id)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, rows)
}
func (h *ClassroomHandler) TeacherSchedule(c *gin.Context) {
	kind, id, ok := actor(c)
	if !ok {
		return
	}
	if kind != "teacher" {
		failure(c, 403, 40300, "无权查看此排班")
		return
	}
	rows, e := h.service.TeacherSchedule(c.Request.Context(), id, c.Query("date"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, rows)
}
func (h *ClassroomHandler) Lesson(c *gin.Context) {
	kind, id, ok := actor(c)
	if !ok {
		return
	}
	lessonID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	row, e := h.service.Lesson(c.Request.Context(), kind, id, lessonID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) Cancel(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !bindJSON(c, &in) {
		return
	}
	_, actorID, ok := actor(c)
	if !ok {
		return
	}
	e := h.service.CancelLesson(c.Request.Context(), id, in.Reason, actorID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *ClassroomHandler) Statistics(c *gin.Context) {
	kind, id, ok := actor(c)
	if !ok {
		return
	}
	if kind == "admin" {
		var e error
		id, e = strconv.ParseUint(c.Param("id"), 10, 64)
		if e != nil || id == 0 {
			failure(c, 400, 40000, "外教编号无效")
			return
		}
		kind = "teacher"
	}
	month := c.DefaultQuery("month", time.Now().Format("2006-01"))
	value, e := h.service.Statistics(c.Request.Context(), kind, id, month)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, value)
}
func (h *ClassroomHandler) Dashboard(c *gin.Context) {
	_, id, ok := actor(c)
	if !ok {
		return
	}
	value, e := h.service.StudentDashboard(c.Request.Context(), id)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, value)
}
func (h *ClassroomHandler) StudentProfile(c *gin.Context) {
	_, id, ok := actor(c)
	if !ok {
		return
	}
	row, e := h.service.StudentProfile(c.Request.Context(), id)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}

func (h *ClassroomHandler) AdminStudentProfile(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	row, e := h.service.StudentProfile(c.Request.Context(), id)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) SaveStudentProfile(c *gin.Context) {
	_, id, ok := actor(c)
	if !ok {
		return
	}
	var in model.StudentProfile
	if !bindJSON(c, &in) {
		return
	}
	row, e := h.service.SaveStudentProfile(c.Request.Context(), id, in)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) TeacherProfile(c *gin.Context) {
	_, id, ok := actor(c)
	if !ok {
		return
	}
	row, e := h.service.Teacher(c.Request.Context(), id)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) SaveTeacherProfile(c *gin.Context) {
	_, id, ok := actor(c)
	if !ok {
		return
	}
	var in service.TeacherInput
	if !bindJSON(c, &in) {
		return
	}
	in.Active = nil
	in.Country = ""
	in.Timezone = ""
	row, e := h.service.UpdateTeacher(c.Request.Context(), id, in, 0)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, row)
}
func (h *ClassroomHandler) Presence(c *gin.Context) {
	kind, id, ok := actor(c)
	if !ok {
		return
	}
	lessonID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	action := "heartbeat"
	if c.FullPath() == "/api/v1/classrooms/:id/leave" || c.FullPath() == "/api/v1/teacher/classrooms/:id/leave" {
		action = "leave"
	}
	if c.Query("event") == "connected" {
		action = "connected"
	}
	if e := h.service.Presence(c.Request.Context(), kind, id, lessonID, action); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}

func (h *ClassroomHandler) Join(c *gin.Context) {
	kind, id, ok := actor(c)
	if !ok {
		return
	}
	lessonID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	credentials, e := h.service.Join(c.Request.Context(), kind, id, lessonID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	success(c, credentials)
}
