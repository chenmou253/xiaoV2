package handler

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"xiaov2/internal/ai"
	"xiaov2/internal/model"
	"xiaov2/internal/repository"
	"xiaov2/internal/service"
)

const identityKey = "identity"

type PlatformHandler struct {
	service *service.PlatformService
	secure  bool
}

func NewPlatformHandler(s *service.PlatformService, secure bool) *PlatformHandler {
	return &PlatformHandler{service: s, secure: secure}
}
func cookieName(kind string) string { return "xiaov2_" + kind + "_session" }
func kind(c *gin.Context) string {
	if strings.HasPrefix(c.FullPath(), "/api/v1/admin/") {
		return "admin"
	}
	if strings.HasPrefix(c.FullPath(), "/api/v1/teacher/") {
		return "teacher"
	}
	return "student"
}
func identity(c *gin.Context) *repository.Identity {
	v, ok := c.Get(identityKey)
	if !ok {
		return nil
	}
	u, _ := v.(repository.Identity)
	return &u
}
func HasPermission(c *gin.Context, p string) bool {
	u := identity(c)
	if u == nil {
		return false
	}
	for _, v := range u.Permissions {
		if v == p {
			return true
		}
	}
	return false
}
func (h *PlatformHandler) Authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		k := kind(c)
		raw, e := c.Cookie(cookieName(k))
		if e == nil {
			if u, e := h.service.Authenticate(c.Request.Context(), k, raw); e == nil {
				c.Set(identityKey, u)
			}
		}
		c.Next()
	}
}
func (h *PlatformHandler) Require(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := identity(c)
		if u == nil {
			failure(c, 401, 40100, "请先登录")
			return
		}
		if u.Kind != "admin" || !HasPermission(c, permission) {
			failure(c, 403, 40300, "没有此操作的权限")
			return
		}
		c.Next()
	}
}
func (h *PlatformHandler) RequireKind(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := identity(c)
		if u == nil {
			failure(c, 401, 40100, "请先登录")
			return
		}
		if u.Kind != kind {
			failure(c, 403, 40300, "没有此操作的权限")
			return
		}
		c.Next()
	}
}
func (h *PlatformHandler) Me(c *gin.Context) { success(c, gin.H{"user": identity(c)}) }
func (h *PlatformHandler) Login(c *gin.Context) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &in) {
		return
	}
	k := kind(c)
	u, raw, e := h.service.Login(c.Request.Context(), k, in.Email, in.Password, c.ClientIP())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: cookieName(k), Value: raw, Path: "/api/v1", MaxAge: 30 * 86400, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode})
	success(c, u)
}
func (h *PlatformHandler) Logout(c *gin.Context) {
	k := kind(c)
	raw, _ := c.Cookie(cookieName(k))
	if e := h.service.Logout(c.Request.Context(), k, raw); e != nil {
		writePlatformError(c, e)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: cookieName(k), Value: "", Path: "/api/v1", MaxAge: -1, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode})
	success(c, gin.H{"ok": true})
}
func (h *PlatformHandler) Register(c *gin.Context) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &in) {
		return
	}
	_, e := h.service.Register(c.Request.Context(), in.Email, in.Password)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"message": "注册成功，请查收验证邮件"})
}
func (h *PlatformHandler) Verify(c *gin.Context) {
	var in struct {
		Token string `json:"token"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.Verify(c.Request.Context(), in.Token); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"message": "邮箱验证成功，请登录"})
}
func (h *PlatformHandler) Forgot(c *gin.Context) {
	var in struct {
		Email string `json:"email"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.Forgot(c.Request.Context(), kind(c), in.Email); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"message": "如果账号存在，密码重置邮件已发送"})
}
func (h *PlatformHandler) Reset(c *gin.Context) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.Reset(c.Request.Context(), kind(c), in.Token, in.Password); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"message": "密码已重置，请重新登录"})
}
func (h *PlatformHandler) Config(c *gin.Context) {
	v, e := h.service.Config(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *PlatformHandler) RBAC(c *gin.Context) {
	v, e := h.service.RBAC(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *PlatformHandler) SaveRole(c *gin.Context) {
	var in struct {
		ID          uint64   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if !bindJSON(c, &in) {
		return
	}
	id, e := h.service.SaveRole(c.Request.Context(), model.Role{ID: in.ID, Name: in.Name, Description: in.Description}, in.Permissions, identity(c).ID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"id": id, "ok": true})
}
func (h *PlatformHandler) CreateAdmin(c *gin.Context) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &in) {
		return
	}
	id, e := h.service.CreateAdmin(c.Request.Context(), in.Email, in.Password, identity(c).ID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"id": id, "ok": true})
}
func (h *PlatformHandler) AssignRoles(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		RoleIDs []uint64 `json:"role_ids"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.AssignRoles(c.Request.Context(), id, in.RoleIDs, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *PlatformHandler) AdminStatus(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Active bool `json:"active"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.SetAdminStatus(c.Request.Context(), id, in.Active, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *PlatformHandler) Students(c *gin.Context) {
	v, e := h.service.Students(c.Request.Context(), c.Query("q"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *PlatformHandler) StudentStatus(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Active bool `json:"active"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.SetStudentStatus(c.Request.Context(), id, in.Active, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *PlatformHandler) SiteSettings(c *gin.Context) {
	v, e := h.service.SiteSettings(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}

func (h *PlatformHandler) UpdateSiteSettingStatus(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Status int8 `json:"status"`
	}
	if !bindJSON(c, &in) {
		return
	}
	v, e := h.service.SetSiteSettingStatus(c.Request.Context(), id, in.Status, identity(c).ID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *PlatformHandler) Models(c *gin.Context) { success(c, h.service.Models()) }
func (h *PlatformHandler) ModelVoices(c *gin.Context) {
	v, e := h.service.ModelVoices(c.Param("modelId"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *PlatformHandler) ModelSettings(c *gin.Context) {
	v, e := h.service.ModelSettings(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *PlatformHandler) SaveModelSettings(c *gin.Context) {
	var in ai.Settings
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.SaveModelSettings(c.Request.Context(), in, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *PlatformHandler) Audit(c *gin.Context) {
	v, e := h.service.Audit(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func bindJSON(c *gin.Context, v any) bool {
	if e := c.ShouldBindJSON(v); e != nil {
		failure(c, 400, 40000, "请求格式不正确")
		return false
	}
	return true
}
func uintParam(c *gin.Context, name string) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param(name), 10, 64)
	if e != nil || v == 0 {
		failure(c, 400, 40000, "无效编号")
		return 0, false
	}
	return v, true
}
func writePlatformError(c *gin.Context, e error) {
	var app *service.AppError
	if errors.As(e, &app) {
		failure(c, app.Status, app.Code, app.Message)
		return
	}
	if errors.Is(e, gorm.ErrRecordNotFound) {
		failure(c, 404, 40400, "记录不存在")
		return
	}
	log.Printf("request failed: %v", e)
	failure(c, 500, 50000, "服务暂时不可用，请稍后重试")
}
