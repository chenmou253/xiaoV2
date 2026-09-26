package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"xiaov2/internal/model"
	"xiaov2/internal/service"
)

type ClassroomHandler struct{ service *service.ClassroomService }

func NewClassroomHandler(s *service.ClassroomService) *ClassroomHandler {
	return &ClassroomHandler{service: s}
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
	success(c, rows)
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
