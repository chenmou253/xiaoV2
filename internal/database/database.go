package database

import (
	"context"
	"fmt"
	"time"

	"xiaov2/internal/config"
	"xiaov2/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(cfg config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	if err != nil {
		return nil, fmt.Errorf("open MySQL %s:%s/%s: %w", cfg.MySQLHost, cfg.MySQLPort, cfg.MySQLDatabase, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("obtain MySQL connection: %w", err)
	}
	sqlDB.SetMaxOpenConns(15)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(3 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping MySQL %s:%s/%s: %w", cfg.MySQLHost, cfg.MySQLPort, cfg.MySQLDatabase, err)
	}
	return db, nil
}

func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.Book{}, &model.BookPage{}, &model.Student{}, &model.Admin{}, &model.Role{}, &model.Permission{}, &model.RolePermission{}, &model.AdminRole{}, &model.StudentSession{}, &model.AdminSession{}, &model.StudentEmailToken{}, &model.AdminEmailToken{}, &model.AuthThrottle{}, &model.SiteSetting{}, &model.AuditLog{}, &model.PageVersion{}, &model.TextbookDraft{}, &model.TextbookDraftPage{}, &model.TextbookJob{}, &model.TextbookAudioItem{}, &model.TextbookAudioAttempt{}); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return seed(db)
}

func seed(db *gorm.DB) error {
	permissions := map[string]string{"admin.access": "进入后台", "rbac.read": "查看角色权限", "rbac.write": "管理角色权限", "site_settings.read": "查看网站设置", "site_settings.write": "修改网站设置", "content.read": "查看教材", "content.write": "编辑教材", "content.review": "审核教材", "content.publish": "发布教材", "users.read": "查看学生", "users.write": "启停学生", "audit.read": "查看操作日志"}
	roles := map[string][]string{"superadmin": {}, "editor": {"admin.access", "content.read", "content.write"}, "reviewer": {"admin.access", "content.read", "content.review"}, "publisher": {"admin.access", "content.read", "content.publish"}, "support": {"admin.access", "users.read", "users.write"}}
	descriptions := map[string]string{"superadmin": "超级管理员", "editor": "教材编辑", "reviewer": "教材审核", "publisher": "发布管理员", "support": "学生客服"}
	for code, description := range permissions {
		if err := db.FirstOrCreate(&model.Permission{Code: code}, model.Permission{Code: code, Description: description}).Error; err != nil {
			return err
		}
		db.Model(&model.Permission{}).Where("code = ?", code).Update("description", description)
	}
	for code := range permissions {
		roles["superadmin"] = append(roles["superadmin"], code)
	}
	for name, perms := range roles {
		var role model.Role
		if err := db.Where("name = ?", name).Attrs(model.Role{Description: descriptions[name], BuiltIn: true}).FirstOrCreate(&role).Error; err != nil {
			return err
		}
		if err := db.Model(&model.Role{}).Where("id = ? AND description = ?", role.ID, name).Update("description", descriptions[name]).Error; err != nil {
			return err
		}
		for _, p := range perms {
			if err := db.FirstOrCreate(&model.RolePermission{RoleID: role.ID, PermissionCode: p}).Error; err != nil {
				return err
			}
		}
	}
	// Migrate existing custom-role grants away from the retired billing menu
	// before removing those permission codes from the RBAC catalog.
	var legacy []model.RolePermission
	if err := db.Where("permission_code IN ?", []string{"settings.read", "settings.write"}).Find(&legacy).Error; err != nil {
		return err
	}
	for _, grant := range legacy {
		mapped := map[string]string{"settings.read": "site_settings.read", "settings.write": "site_settings.write"}[grant.PermissionCode]
		if mapped == "" {
			continue
		}
		if err := db.FirstOrCreate(&model.RolePermission{RoleID: grant.RoleID, PermissionCode: mapped}).Error; err != nil {
			return err
		}
	}
	retired := []string{"settings.read", "settings.write", "entitlements.read", "entitlements.write"}
	if err := db.Where("permission_code IN ?", retired).Delete(&model.RolePermission{}).Error; err != nil {
		return err
	}
	if err := db.Where("code IN ?", retired).Delete(&model.Permission{}).Error; err != nil {
		return err
	}
	modelSettings := []model.SiteSetting{
		{DictCode: "ai.ocr_model", ItemLabel: "OCR 模型", ItemValue: "local-paddleocr", Sort: 10, Status: 1},
		{DictCode: "ai.tts_model", ItemLabel: "TTS 模型", ItemValue: "local-qwen3-tts", Sort: 20, Status: 1},
		{DictCode: "ai.tts_voice", ItemLabel: "TTS 音色", ItemValue: "aiden", Sort: 30, Status: 1},
	}
	for index := range modelSettings {
		row := modelSettings[index]
		if err := db.Where("dict_code = ?", row.DictCode).FirstOrCreate(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
