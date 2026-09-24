package router

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"xiaov2/internal/handler"
)

func New(db *gorm.DB, books *handler.BookHandler, webRoot, mode string, extras ...any) *gin.Engine {
	var platform *handler.PlatformHandler
	var editor *handler.EditorHandler
	var origin string
	for _, extra := range extras {
		switch value := extra.(type) {
		case *handler.PlatformHandler:
			platform = value
		case *handler.EditorHandler:
			editor = value
		case string:
			origin = value
		}
	}
	gin.SetMode(mode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), securityHeaders(), requestGuard(origin))
	_ = r.SetTrustedProxies(nil)
	r.GET("/healthz", func(c *gin.Context) {
		sqlDB, err := db.DB()
		if err == nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			err = sqlDB.PingContext(ctx)
		}
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	api := r.Group("/api/v1")
	if platform != nil {
		api.Use(platform.Authenticate())
		api.GET("/me", platform.Me)
		api.POST("/auth/login", platform.Login)
		api.POST("/auth/logout", platform.Logout)
		api.POST("/auth/register", platform.Register)
		api.POST("/auth/verify", platform.Verify)
		api.POST("/auth/forgot", platform.Forgot)
		api.POST("/auth/reset", platform.Reset)
		api.GET("/config", platform.Config)
	}
	api.GET("/books", books.List)
	api.GET("/books/:bookId", books.Get)
	api.GET("/books/:bookId/cover", books.Cover)
	api.GET("/books/:bookId/pages", books.Pages)
	api.GET("/books/:bookId/pages/:page", books.Page)
	api.GET("/books/:bookId/pages/:page/image", books.PageImage)
	api.GET("/books/:bookId/pages/:page/audio/:itemId", books.Audio)
	if platform != nil && editor != nil {
		api.GET("/admin/me", platform.Me)
		api.POST("/admin/auth/login", platform.Login)
		api.POST("/admin/auth/logout", platform.Logout)
		api.POST("/admin/auth/forgot", platform.Forgot)
		api.POST("/admin/auth/reset", platform.Reset)
		admin := api.Group("/admin", platform.Require("admin.access"))
		// Draft and job state changes asynchronously. Never let a browser reuse a
		// queued/running response after the worker has already finished the task.
		admin.Use(func(c *gin.Context) {
			c.Header("Cache-Control", "private, no-store")
			c.Next()
		})
		admin.POST("/accounts", platform.Require("rbac.write"), platform.CreateAdmin)
		admin.PUT("/accounts/:id/status", platform.Require("rbac.write"), platform.AdminStatus)
		admin.GET("/rbac", platform.Require("rbac.read"), platform.RBAC)
		admin.POST("/roles", platform.Require("rbac.write"), platform.SaveRole)
		admin.PUT("/accounts/:id/roles", platform.Require("rbac.write"), platform.AssignRoles)
		admin.GET("/students", platform.Require("users.read"), platform.Students)
		admin.PUT("/students/:id/status", platform.Require("users.write"), platform.StudentStatus)
		admin.GET("/site-settings", platform.Require("site_settings.read"), platform.SiteSettings)
		admin.PUT("/site-settings/:id/status", platform.Require("site_settings.write"), platform.UpdateSiteSettingStatus)
		admin.GET("/models", platform.Require("site_settings.read"), platform.Models)
		admin.GET("/models/:modelId/voices", platform.Require("site_settings.read"), platform.ModelVoices)
		admin.GET("/settings/models", platform.Require("site_settings.read"), platform.ModelSettings)
		admin.PUT("/settings/models", platform.Require("site_settings.write"), platform.SaveModelSettings)
		admin.GET("/books", platform.Require("content.read"), editor.Books)
		admin.PUT("/books/:bookId", platform.Require("content.publish"), editor.SaveBook)
		admin.GET("/books/:bookId/pages", platform.Require("content.read"), editor.PublishedPages)
		admin.GET("/books/:bookId/pages/:page", platform.Require("content.read"), editor.PublishedPage)
		admin.GET("/books/:bookId/pages/:page/image", platform.Require("content.read"), books.PageImage)
		admin.PUT("/books/:bookId/pages/:page", platform.Require("content.write"), immutablePublished)
		admin.PUT("/books/:bookId/order", platform.Require("content.write"), immutablePublished)
		admin.GET("/audit", platform.Require("audit.read"), platform.Audit)
		admin.GET("/drafts", platform.Require("content.read"), editor.List)
		admin.GET("/audio-review", platform.Require("content.read"), editor.AudioReviewQueue)
		admin.POST("/drafts", platform.Require("content.write"), editor.Upload)
		admin.POST("/books/:bookId/drafts", platform.Require("content.write"), editor.Copy)
		admin.GET("/drafts/:draftId/models", platform.Require("content.read"), editor.ModelOptions)
		admin.GET("/drafts/:draftId/models/:modelId/voices", platform.Require("content.read"), editor.ModelVoices)
		admin.GET("/drafts/:draftId", platform.Require("content.read"), editor.Get)
		admin.GET("/drafts/:draftId/status", platform.Require("content.read"), editor.Status)
		admin.DELETE("/drafts/:draftId", platform.Require("content.write"), editor.Delete)
		admin.PUT("/drafts/:draftId", platform.Require("content.write"), editor.SaveMeta)
		admin.PUT("/drafts/:draftId/models", platform.Require("content.write"), editor.SwitchModels)
		admin.PUT("/drafts/:draftId/order", platform.Require("content.write"), editor.Order)
		admin.GET("/drafts/:draftId/pages/:page", platform.Require("content.read"), editor.Page)
		admin.GET("/drafts/:draftId/pages/:page/image", platform.Require("content.read"), editor.Image)
		admin.GET("/drafts/:draftId/pages/:page/audio/:itemId", platform.Require("content.read"), editor.Audio)
		admin.POST("/drafts/:draftId/pages/:page/audio/:itemId/regenerate", platform.Require("content.write"), editor.RegenerateAudioItem)
		admin.POST("/drafts/:draftId/pages/:page/audio/:itemId/upload", platform.Require("content.write"), editor.UploadAudioItem)
		admin.GET("/drafts/:draftId/pages/:page/audio-issues", platform.Require("content.read"), editor.AudioIssues)
		admin.GET("/drafts/:draftId/pages/:page/translation-issues", platform.Require("content.read"), editor.TranslationIssues)
		admin.PUT("/drafts/:draftId/pages/:page/translation-issues/:itemId", platform.Require("content.write"), editor.ResolveTranslationIssue)
		admin.GET("/drafts/:draftId/pages/:page/audio-issues/:itemId/failed", platform.Require("content.read"), editor.FailedAudio)
		admin.POST("/drafts/:draftId/pages/:page/audio-issues/:itemId/retry", platform.Require("content.write"), editor.RetryAudioIssue)
		admin.POST("/drafts/:draftId/pages/:page/audio-issues/:itemId/approve", platform.Require("content.write"), editor.ApproveAudioIssue)
		admin.PUT("/drafts/:draftId/pages/:page", platform.Require("content.write"), editor.SavePage)
		admin.POST("/drafts/:draftId/actions/:action", platform.Require("content.read"), editor.Action)
	}
	assets := filepath.Join(webRoot, "assets")
	if info, err := os.Stat(assets); err == nil && info.IsDir() {
		r.Static("/assets", assets)
	}
	index := filepath.Join(webRoot, "index.html")
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"code": 40400, "message": "not found", "data": nil})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusNotFound)
			return
		}
		if _, err := os.Stat(index); err != nil {
			c.String(http.StatusServiceUnavailable, "frontend build not found; run npm --prefix web run build")
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.File(index)
	})
	return r
}

func immutablePublished(c *gin.Context) {
	c.JSON(http.StatusConflict, gin.H{"code": 40900, "message": "请创建草稿，完成审核后发布", "data": nil})
}
func requestGuard(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := int64(8 << 20)
		if c.Request.Method == http.MethodPost && c.Request.URL.Path == "/api/v1/admin/drafts" {
			limit = 129 << 20
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions {
			if c.GetHeader("X-Requested-With") != "xiaov2-web" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 40300, "message": "请求来源校验失败", "data": nil})
				return
			}
			if value := strings.TrimRight(c.GetHeader("Origin"), "/"); value != "" && origin != "" && value != origin {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 40300, "message": "请求来源不匹配", "data": nil})
				return
			}
		}
		c.Next()
	}
}
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'")
		c.Next()
	}
}
