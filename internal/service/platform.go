package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"xiaov2/internal/ai"
	"xiaov2/internal/config"
	"xiaov2/internal/model"
	"xiaov2/internal/repository"
)

type AppError struct {
	Status, Code int
	Message      string
}

func (e *AppError) Error() string       { return e.Message }
func bad(message string) *AppError      { return &AppError{400, 40000, message} }
func conflict(message string) *AppError { return &AppError{409, 40900, message} }
func notFound(message string) *AppError { return &AppError{404, 40400, message} }

type PlatformService struct {
	repo           *repository.PlatformRepository
	cfg            config.Config
	ttsModelSwitch func(oldModel, newModel string) error
}

func NewPlatformService(repo *repository.PlatformRepository, cfg config.Config) *PlatformService {
	return &PlatformService{repo: repo, cfg: cfg}
}

func (s *PlatformService) SetTTSModelSwitchHook(hook func(oldModel, newModel string) error) {
	s.ttsModelSwitch = hook
}
func normalizeEmail(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	a, e := mail.ParseAddress(s)
	if e != nil || a.Address != s || len(s) > 254 {
		return ""
	}
	return s
}
func validPassword(s string) bool { return utf8.RuneCountInString(s) >= 10 && len(s) <= 72 }
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}

func (s *PlatformService) Authenticate(ctx context.Context, kind, raw string) (repository.Identity, error) {
	if len(raw) != 64 {
		return repository.Identity{}, gorm.ErrRecordNotFound
	}
	return s.repo.ResolveSession(ctx, kind, raw)
}
func (s *PlatformService) Login(ctx context.Context, kind, email, password, remote string) (repository.Identity, string, error) {
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return repository.Identity{}, "", bad("邮箱或密码错误")
	}
	if !s.repo.Throttle(ctx, "login:"+kind+":"+remote+":"+email, 8, 15*time.Minute) {
		return repository.Identity{}, "", &AppError{429, 42900, "尝试次数过多，请稍后再试"}
	}
	id, hash, verified, active, e := s.repo.Account(ctx, kind, email)
	if e != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return repository.Identity{}, "", bad("邮箱或密码错误")
	}
	if !active {
		return repository.Identity{}, "", &AppError{403, 40300, "账号已停用"}
	}
	if !verified {
		return repository.Identity{}, "", &AppError{403, 40300, "请先完成邮箱验证"}
	}
	raw, e := randomToken()
	if e != nil {
		return repository.Identity{}, "", e
	}
	if e = s.repo.CreateSession(ctx, kind, raw, id, time.Now().UTC().Add(30*24*time.Hour)); e != nil {
		return repository.Identity{}, "", e
	}
	identity, e := s.repo.ResolveSession(ctx, kind, raw)
	return identity, raw, e
}
func (s *PlatformService) Logout(ctx context.Context, kind, raw string) error {
	if raw == "" {
		return nil
	}
	return s.repo.DeleteSession(ctx, kind, raw)
}
func (s *PlatformService) Register(ctx context.Context, email, password string) (string, error) {
	email = normalizeEmail(email)
	if email == "" || !validPassword(password) {
		return "", bad("请填写有效邮箱；密码至少10个字符且不超过72字节")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return "", e
	}
	id, e := s.repo.CreateStudent(ctx, email, string(hash))
	if e != nil {
		return "", conflict("邮箱已注册")
	}
	token, e := s.issueToken(ctx, "student", "verify", id, email)
	if e != nil {
		return "", e
	}
	return token, nil
}
func (s *PlatformService) Verify(ctx context.Context, token string) error {
	if len(token) != 64 {
		return bad("验证链接无效或已过期")
	}
	if e := s.repo.ConsumeEmailToken(ctx, "student", "verify", token, ""); e != nil {
		return bad("验证链接无效或已过期")
	}
	return nil
}
func (s *PlatformService) Forgot(ctx context.Context, kind, email string) error {
	email = normalizeEmail(email)
	if email == "" {
		return nil
	}
	if !s.repo.Throttle(ctx, "mail:"+kind+":"+email, 4, time.Hour) {
		return &AppError{429, 42900, "请求过于频繁，请稍后再试"}
	}
	id, _, _, active, e := s.repo.Account(ctx, kind, email)
	if errors.Is(e, gorm.ErrRecordNotFound) || !active {
		return nil
	}
	if e != nil {
		return e
	}
	_, e = s.issueToken(ctx, kind, "reset", id, email)
	return e
}
func (s *PlatformService) Reset(ctx context.Context, kind, token, password string) error {
	if len(token) != 64 || !validPassword(password) {
		return bad("重置链接无效，或密码不足10个字符")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return e
	}
	if e = s.repo.ConsumeEmailToken(ctx, kind, "reset", token, string(hash)); e != nil {
		return bad("重置链接无效或已过期")
	}
	return nil
}
func (s *PlatformService) issueToken(ctx context.Context, kind, purpose string, id uint64, email string) (string, error) {
	raw, e := randomToken()
	if e != nil {
		return "", e
	}
	if e = s.repo.SaveEmailToken(ctx, kind, purpose, raw, id, time.Now().UTC().Add(time.Hour)); e != nil {
		return "", e
	}
	path := "account"
	if kind == "admin" {
		path = "admin/login"
	}
	link := fmt.Sprintf("%s/%s?mode=%s&token=%s", s.cfg.AppOrigin, path, purpose, raw)
	subject, body := "验证邮箱", "请打开以下链接完成邮箱验证：\n"+link
	if purpose == "reset" {
		subject = "重置密码"
		body = "请打开以下链接重置密码（1小时内有效）：\n" + link
	}
	if e = s.sendMail(email, subject, body); e != nil {
		return "", e
	}
	return raw, nil
}
func (s *PlatformService) sendMail(to, subject, body string) error {
	if s.cfg.DevMailDir != "" {
		if e := os.MkdirAll(s.cfg.DevMailDir, 0700); e != nil {
			return e
		}
		b, _ := json.MarshalIndent(map[string]string{"to": to, "subject": subject, "body": body}, "", "  ")
		return os.WriteFile(filepath.Join(s.cfg.DevMailDir, fmt.Sprintf("%d.json", time.Now().UnixNano())), b, 0600)
	}
	if s.cfg.SMTPHost == "" || s.cfg.SMTPFrom == "" {
		return errors.New("email service is not configured")
	}
	auth := smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPassword, s.cfg.SMTPHost)
	message := []byte("To: " + to + "\r\nSubject: " + subject + "\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body)
	return smtp.SendMail(s.cfg.SMTPHost+":"+s.cfg.SMTPPort, auth, s.cfg.SMTPFrom, []string{to}, message)
}

func (s *PlatformService) EmailMode() string {
	if s.cfg.DevMailDir != "" {
		return "local"
	}
	if s.cfg.SMTPHost != "" && s.cfg.SMTPFrom != "" {
		return "smtp"
	}
	return "disabled"
}
func (s *PlatformService) Config(ctx context.Context) (map[string]any, error) {
	return map[string]any{"email_enabled": s.EmailMode() != "disabled", "email_mode": s.EmailMode()}, nil
}
func (s *PlatformService) RBAC(ctx context.Context) (repository.RBACData, error) {
	return s.repo.RBAC(ctx)
}

var rolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,39}$`)
var KnownPermissions = map[string]bool{"admin.access": true, "rbac.read": true, "rbac.write": true, "site_settings.read": true, "site_settings.write": true, "content.read": true, "content.write": true, "content.review": true, "content.publish": true, "users.read": true, "users.write": true, "audit.read": true}

func (s *PlatformService) SaveRole(ctx context.Context, role model.Role, perms []string, actor uint64) (uint64, error) {
	if !rolePattern.MatchString(role.Name) || role.Name == "superadmin" || len(role.Description) > 255 {
		return 0, bad("角色标识格式无效")
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, p := range perms {
		if !KnownPermissions[p] {
			return 0, bad("存在无效权限")
		}
		if !seen[p] {
			seen[p] = true
			clean = append(clean, p)
		}
	}
	id, e := s.repo.SaveRole(ctx, role, clean, actor)
	if errors.Is(e, repository.ErrConflict) {
		return 0, conflict("系统角色不可修改")
	}
	return id, e
}
func (s *PlatformService) CreateAdmin(ctx context.Context, email, password string, actor uint64) (uint64, error) {
	email = normalizeEmail(email)
	if email == "" || !validPassword(password) {
		return 0, bad("管理员邮箱无效，密码至少10个字符")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return 0, e
	}
	id, e := s.repo.CreateAdmin(ctx, email, string(hash), actor)
	if e != nil {
		return 0, conflict("管理员邮箱已存在")
	}
	return id, nil
}
func (s *PlatformService) AssignRoles(ctx context.Context, id uint64, roles []uint64, actor uint64) error {
	e := s.repo.AssignRoles(ctx, id, roles, actor)
	if errors.Is(e, repository.ErrConflict) {
		return conflict("不能修改自己或超级管理员的角色")
	}
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return notFound("管理员或角色不存在")
	}
	return e
}
func (s *PlatformService) SetAdminStatus(ctx context.Context, id uint64, active bool, actor uint64) error {
	e := s.repo.SetAdminStatus(ctx, id, active, actor)
	if errors.Is(e, repository.ErrConflict) {
		return conflict("不能停用自己或超级管理员")
	}
	return e
}
func (s *PlatformService) Students(ctx context.Context, q string) ([]model.Student, error) {
	return s.repo.Students(ctx, q)
}
func (s *PlatformService) SetStudentStatus(ctx context.Context, id uint64, active bool, actor uint64) error {
	return s.repo.SetStudentStatus(ctx, id, active, actor)
}
func (s *PlatformService) SiteSettings(ctx context.Context) ([]model.SiteSetting, error) {
	return s.repo.SiteSettings(ctx)
}
func (s *PlatformService) SetSiteSettingStatus(ctx context.Context, id uint64, status int8, actor uint64) (model.SiteSetting, error) {
	if status != 0 && status != 1 {
		return model.SiteSetting{}, bad("设置状态只能是 0 或 1")
	}
	return s.repo.SetSiteSettingStatus(ctx, id, status, actor)
}
func (s *PlatformService) Models() []ai.Model { return ai.Models() }
func (s *PlatformService) ModelVoices(id string) ([]ai.Voice, error) {
	item, ok := ai.Find(id)
	if !ok || item.Type != "tts" {
		return nil, notFound("TTS 模型不存在")
	}
	return ai.Voices(id), nil
}
func (s *PlatformService) ModelSettings(ctx context.Context) (ai.Settings, error) {
	return s.repo.ModelSettings(ctx)
}
func (s *PlatformService) SaveModelSettings(ctx context.Context, value ai.Settings, actor uint64) error {
	value = ai.NormalizeSettings(value)
	current, err := s.repo.ModelSettings(ctx)
	if err != nil {
		return err
	}
	ocr, ok := ai.Find(value.OCRModel)
	if !ok || ocr.Type != "ocr" || !ocr.Enabled {
		return bad("OCR 模型无效")
	}
	ttsModel, ok := ai.Find(value.TTSModel)
	if !ok || ttsModel.Type != "tts" || !ttsModel.Enabled {
		return bad("TTS 模型无效")
	}
	if !ocr.Available {
		return bad("OCR 模型当前不可用：" + ocr.UnavailableReason)
	}
	if !ttsModel.Available {
		return bad("TTS 模型当前不可用：" + ttsModel.UnavailableReason)
	}
	if !ai.ValidVoice(value.TTSModel, value.TTSVoice) {
		return bad("TTS 音色不属于所选模型")
	}
	if current.TTSModel != value.TTSModel && s.ttsModelSwitch != nil {
		if err := s.ttsModelSwitch(current.TTSModel, value.TTSModel); err != nil {
			return err
		}
	}
	return s.repo.SaveModelSettings(ctx, value, actor)
}
func (s *PlatformService) Audit(ctx context.Context) ([]repository.AuditRow, error) {
	return s.repo.Audit(ctx)
}
func (s *PlatformService) Bootstrap(ctx context.Context, email, password string) error {
	email = normalizeEmail(email)
	if email == "" || utf8.RuneCountInString(password) < 12 || len(password) > 72 {
		return bad("bootstrap 需要有效 ADMIN_EMAIL 和 12–72 字节 ADMIN_PASSWORD")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return e
	}
	return s.repo.Bootstrap(ctx, email, string(hash))
}
