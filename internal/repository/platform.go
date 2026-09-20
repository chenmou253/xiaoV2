package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/ai"
	"xiaov2/internal/model"
)

var ErrConflict = errors.New("conflict")

type Identity struct {
	ID          uint64   `json:"id"`
	Email       string   `json:"email"`
	Kind        string   `json:"kind"`
	Permissions []string `json:"permissions"`
}
type PlatformRepository struct{ db *gorm.DB }

func NewPlatformRepository(db *gorm.DB) *PlatformRepository { return &PlatformRepository{db: db} }
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (r *PlatformRepository) Account(ctx context.Context, kind, email string) (uint64, string, bool, bool, error) {
	if kind == "admin" {
		var a model.Admin
		e := r.db.WithContext(ctx).Where("email = ?", email).First(&a).Error
		return a.ID, a.PasswordHash, a.Verified, a.Active, e
	}
	var a model.Student
	e := r.db.WithContext(ctx).Where("email = ?", email).First(&a).Error
	return a.ID, a.PasswordHash, a.Verified, a.Active, e
}
func (r *PlatformRepository) CreateStudent(ctx context.Context, email, hash string) (uint64, error) {
	a := model.Student{Email: email, PasswordHash: hash}
	e := r.db.WithContext(ctx).Create(&a).Error
	return a.ID, e
}
func (r *PlatformRepository) CreateAdmin(ctx context.Context, email, hash string, actor uint64) (uint64, error) {
	var id uint64
	e := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		a := model.Admin{Email: email, PasswordHash: hash, Verified: true}
		if x := tx.Create(&a).Error; x != nil {
			return x
		}
		id = a.ID
		return audit(tx, actor, "admin.create", email, map[string]any{"id": id})
	})
	return id, e
}
func (r *PlatformRepository) SaveEmailToken(ctx context.Context, kind, purpose, raw string, id uint64, expires time.Time) error {
	h := HashToken(raw)
	if kind == "admin" {
		return r.db.WithContext(ctx).Create(&model.AdminEmailToken{TokenHash: h, AccountID: id, Purpose: purpose, ExpiresAt: expires}).Error
	}
	return r.db.WithContext(ctx).Create(&model.StudentEmailToken{TokenHash: h, AccountID: id, Purpose: purpose, ExpiresAt: expires}).Error
}
func (r *PlatformRepository) ConsumeEmailToken(ctx context.Context, kind, purpose, raw, newHash string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		h := HashToken(raw)
		var id uint64
		if kind == "admin" {
			var t model.AdminEmailToken
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash=? AND purpose=? AND expires_at>?", h, purpose, time.Now().UTC()).First(&t).Error; e != nil {
				return e
			}
			id = t.AccountID
			if purpose == "verify" {
				if e := tx.Model(&model.Admin{}).Where("id=?", id).Update("verified", true).Error; e != nil {
					return e
				}
			} else {
				if e := tx.Model(&model.Admin{}).Where("id=?", id).Updates(map[string]any{"password_hash": newHash, "verified": true}).Error; e != nil {
					return e
				}
				if e := tx.Where("account_id=?", id).Delete(&model.AdminSession{}).Error; e != nil {
					return e
				}
			}
			return tx.Where("account_id=?", id).Delete(&model.AdminEmailToken{}).Error
		}
		var t model.StudentEmailToken
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash=? AND purpose=? AND expires_at>?", h, purpose, time.Now().UTC()).First(&t).Error; e != nil {
			return e
		}
		id = t.AccountID
		if purpose == "verify" {
			if e := tx.Model(&model.Student{}).Where("id=?", id).Update("verified", true).Error; e != nil {
				return e
			}
		} else {
			if e := tx.Model(&model.Student{}).Where("id=?", id).Updates(map[string]any{"password_hash": newHash, "verified": true}).Error; e != nil {
				return e
			}
			if e := tx.Where("account_id=?", id).Delete(&model.StudentSession{}).Error; e != nil {
				return e
			}
		}
		return tx.Where("account_id=?", id).Delete(&model.StudentEmailToken{}).Error
	})
}
func (r *PlatformRepository) CreateSession(ctx context.Context, kind, raw string, id uint64, expires time.Time) error {
	h := HashToken(raw)
	if kind == "admin" {
		return r.db.WithContext(ctx).Create(&model.AdminSession{TokenHash: h, AccountID: id, ExpiresAt: expires}).Error
	}
	return r.db.WithContext(ctx).Create(&model.StudentSession{TokenHash: h, AccountID: id, ExpiresAt: expires}).Error
}
func (r *PlatformRepository) DeleteSession(ctx context.Context, kind, raw string) error {
	h := HashToken(raw)
	if kind == "admin" {
		return r.db.WithContext(ctx).Where("token_hash=?", h).Delete(&model.AdminSession{}).Error
	}
	return r.db.WithContext(ctx).Where("token_hash=?", h).Delete(&model.StudentSession{}).Error
}
func (r *PlatformRepository) ResolveSession(ctx context.Context, kind, raw string) (Identity, error) {
	h := HashToken(raw)
	var out Identity
	out.Kind = kind
	if kind == "admin" {
		var a model.Admin
		e := r.db.WithContext(ctx).Table("admin_sessions s").Select("a.id,a.email").Joins("JOIN admins a ON a.id=s.account_id").Where("s.token_hash=? AND s.expires_at>? AND a.active=1 AND a.verified=1", h, time.Now().UTC()).Scan(&a).Error
		if e != nil || a.ID == 0 {
			return out, gorm.ErrRecordNotFound
		}
		out.ID, out.Email = a.ID, a.Email
		if e := r.db.WithContext(ctx).Table("admin_roles ar").Select("DISTINCT rp.permission_code").Joins("JOIN role_permissions rp ON rp.role_id=ar.role_id").Where("ar.admin_id=?", a.ID).Order("rp.permission_code").Scan(&out.Permissions).Error; e != nil {
			return out, e
		}
		return out, nil
	}
	var a model.Student
	e := r.db.WithContext(ctx).Table("student_sessions s").Select("a.id,a.email").Joins("JOIN students a ON a.id=s.account_id").Where("s.token_hash=? AND s.expires_at>? AND a.active=1 AND a.verified=1", h, time.Now().UTC()).Scan(&a).Error
	if e != nil || a.ID == 0 {
		return out, gorm.ErrRecordNotFound
	}
	out.ID, out.Email = a.ID, a.Email
	out.Permissions = []string{}
	return out, nil
}
func (r *PlatformRepository) Throttle(ctx context.Context, key string, limit int, window time.Duration) bool {
	now := time.Now().UTC()
	allowed := false
	_ = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t model.AuthThrottle
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", key).Limit(1).Find(&t)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			allowed = true
			return tx.Create(&model.AuthThrottle{ID: key, Attempts: 1, ExpiresAt: now.Add(window)}).Error
		}
		if now.After(t.ExpiresAt) {
			allowed = true
			return tx.Model(&t).Updates(map[string]any{"attempts": 1, "expires_at": now.Add(window)}).Error
		}
		if t.Attempts < limit {
			allowed = true
			return tx.Model(&t).Update("attempts", t.Attempts+1).Error
		}
		return nil
	})
	return allowed
}

type RBACData struct {
	Roles           []model.Role           `json:"roles"`
	Permissions     []model.Permission     `json:"permissions"`
	RolePermissions []model.RolePermission `json:"role_permissions"`
	AdminRoles      []model.AdminRole      `json:"admin_roles"`
	Admins          []model.Admin          `json:"admins"`
}

func (r *PlatformRepository) RBAC(ctx context.Context) (RBACData, error) {
	var d RBACData
	db := r.db.WithContext(ctx)
	for _, q := range []struct {
		v     any
		order string
	}{{&d.Roles, "id"}, {&d.Permissions, "code"}, {&d.RolePermissions, "role_id,permission_code"}, {&d.AdminRoles, "admin_id,role_id"}, {&d.Admins, "id DESC"}} {
		if e := db.Order(q.order).Find(q.v).Error; e != nil {
			return d, e
		}
	}
	return d, nil
}
func (r *PlatformRepository) SaveRole(ctx context.Context, role model.Role, perms []string, actor uint64) (uint64, error) {
	e := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if role.ID > 0 {
			var old model.Role
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&old, role.ID).Error; e != nil {
				return e
			}
			if old.Name == "superadmin" {
				return ErrConflict
			}
			if old.BuiltIn {
				role.Name = old.Name
			}
			if e := tx.Model(&old).Updates(map[string]any{"name": role.Name, "description": role.Description}).Error; e != nil {
				return e
			}
		} else {
			if e := tx.Create(&role).Error; e != nil {
				return e
			}
		}
		if e := tx.Where("role_id=?", role.ID).Delete(&model.RolePermission{}).Error; e != nil {
			return e
		}
		for _, p := range perms {
			if e := tx.Create(&model.RolePermission{RoleID: role.ID, PermissionCode: p}).Error; e != nil {
				return e
			}
		}
		return audit(tx, actor, "role.save", role.Name, perms)
	})
	return role.ID, e
}
func (r *PlatformRepository) AssignRoles(ctx context.Context, adminID uint64, roleIDs []uint64, actor uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if adminID == actor {
			return ErrConflict
		}
		var roles []model.Role
		if len(roleIDs) > 0 {
			if e := tx.Where("id IN ?", roleIDs).Find(&roles).Error; e != nil {
				return e
			}
			if len(roles) != len(roleIDs) {
				return gorm.ErrRecordNotFound
			}
			for _, x := range roles {
				if x.Name == "superadmin" {
					return ErrConflict
				}
			}
		}
		var n int64
		tx.Table("admin_roles ar").Joins("JOIN roles r ON r.id=ar.role_id").Where("ar.admin_id=? AND r.name='superadmin'", adminID).Count(&n)
		if n > 0 {
			return ErrConflict
		}
		if e := tx.Where("admin_id=?", adminID).Delete(&model.AdminRole{}).Error; e != nil {
			return e
		}
		for _, id := range roleIDs {
			if e := tx.Create(&model.AdminRole{AdminID: adminID, RoleID: id}).Error; e != nil {
				return e
			}
		}
		return audit(tx, actor, "admin.roles", stringJSON(adminID), roleIDs)
	})
}
func (r *PlatformRepository) SetAdminStatus(ctx context.Context, id uint64, active bool, actor uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if id == actor {
			return ErrConflict
		}
		var n int64
		tx.Table("admin_roles ar").Joins("JOIN roles r ON r.id=ar.role_id").Where("ar.admin_id=? AND r.name='superadmin'", id).Count(&n)
		if n > 0 {
			return ErrConflict
		}
		res := tx.Model(&model.Admin{}).Where("id=?", id).Update("active", active)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if !active {
			if e := tx.Where("account_id=?", id).Delete(&model.AdminSession{}).Error; e != nil {
				return e
			}
		}
		return audit(tx, actor, "admin.status", stringJSON(id), map[string]bool{"active": active})
	})
}
func (r *PlatformRepository) Students(ctx context.Context, q string) ([]model.Student, error) {
	var out []model.Student
	e := r.db.WithContext(ctx).Where("email LIKE ?", "%"+q+"%").Order("id DESC").Limit(200).Find(&out).Error
	return out, e
}
func (r *PlatformRepository) SetStudentStatus(ctx context.Context, id uint64, active bool, actor uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Student{}).Where("id=?", id).Update("active", active)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if !active {
			if e := tx.Where("account_id=?", id).Delete(&model.StudentSession{}).Error; e != nil {
				return e
			}
		}
		return audit(tx, actor, "student.status", stringJSON(id), map[string]bool{"active": active})
	})
}
func (r *PlatformRepository) SiteSettings(ctx context.Context) ([]model.SiteSetting, error) {
	var settings []model.SiteSetting
	e := r.db.WithContext(ctx).Order("dict_code,sort,id").Find(&settings).Error
	return settings, e
}

func (r *PlatformRepository) SetSiteSettingStatus(ctx context.Context, id uint64, status int8, actor uint64) (model.SiteSetting, error) {
	var out model.SiteSetting
	e := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&out, id).Error; e != nil {
			return e
		}
		if e := tx.Model(&out).Update("status", status).Error; e != nil {
			return e
		}
		return audit(tx, actor, "site_settings.status", out.DictCode+":"+out.ItemValue, map[string]any{
			"id": id, "status": status,
		})
	})
	return out, e
}

func (r *PlatformRepository) ModelSettings(ctx context.Context) (ai.Settings, error) {
	settings := ai.DefaultSettings()
	var rows []model.SiteSetting
	if e := r.db.WithContext(ctx).Where("dict_code IN ? AND status=1", []string{"ai.ocr_model", "ai.tts_model", "ai.tts_voice"}).Order("id").Find(&rows).Error; e != nil {
		return settings, e
	}
	for _, row := range rows {
		switch row.DictCode {
		case "ai.ocr_model":
			settings.OCRModel = row.ItemValue
		case "ai.tts_model":
			settings.TTSModel = row.ItemValue
		case "ai.tts_voice":
			settings.TTSVoice = row.ItemValue
		}
	}
	return ai.NormalizeSettings(settings), nil
}

func (r *PlatformRepository) SaveModelSettings(ctx context.Context, settings ai.Settings, actor uint64) error {
	values := map[string]string{
		"ai.ocr_model": settings.OCRModel,
		"ai.tts_model": settings.TTSModel,
		"ai.tts_voice": settings.TTSVoice,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before := map[string]string{}
		for code, value := range values {
			var row model.SiteSetting
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("dict_code=?", code).First(&row).Error; e != nil {
				return e
			}
			before[code] = row.ItemValue
			if e := tx.Model(&row).Updates(map[string]any{"item_value": value, "status": 1}).Error; e != nil {
				return e
			}
		}
		return audit(tx, actor, "site_settings.models", "ai.models", map[string]any{"before": before, "after": values})
	})
}

type AuditRow struct {
	model.AuditLog
	Actor string `json:"actor"`
}

func (r *PlatformRepository) Audit(ctx context.Context) ([]AuditRow, error) {
	var x []AuditRow
	e := r.db.WithContext(ctx).Table("audit_logs a").Select("a.*,u.email actor").Joins("JOIN admins u ON u.id=a.actor_id").Order("a.id DESC").Limit(200).Scan(&x).Error
	return x, e
}

func (r *PlatformRepository) Bootstrap(ctx context.Context, email, hash string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		tx.Table("admin_roles ar").Joins("JOIN roles r ON r.id=ar.role_id").Where("r.name='superadmin'").Count(&n)
		if n > 0 {
			return nil
		}
		a := model.Admin{Email: email, PasswordHash: hash, Verified: true}
		if e := tx.Create(&a).Error; e != nil {
			return e
		}
		var role model.Role
		if e := tx.Where("name='superadmin'").First(&role).Error; e != nil {
			return e
		}
		return tx.Create(&model.AdminRole{AdminID: a.ID, RoleID: role.ID}).Error
	})
}
func audit(tx *gorm.DB, actor uint64, action, target string, detail any) error {
	raw, _ := json.Marshal(detail)
	return tx.Create(&model.AuditLog{ActorID: actor, Action: action, Target: target, Detail: string(raw)}).Error
}
func stringJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
