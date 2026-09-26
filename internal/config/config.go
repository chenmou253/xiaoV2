package config

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	driver "github.com/go-sql-driver/mysql"
)

type Config struct {
	Addr                     string
	MySQLHost                string
	MySQLPort                string
	MySQLUser                string
	MySQLPassword            string
	MySQLDatabase            string
	ResourceRoot             string
	WebRoot                  string
	GinMode                  string
	AppOrigin                string
	DevMailDir               string
	SMTPHost                 string
	SMTPPort                 string
	SMTPUser                 string
	SMTPPassword             string
	SMTPFrom                 string
	AdminEmail               string
	AdminPassword            string
	EditorRoot               string
	Python                   string
	AgoraAppID               string
	AgoraAppCertificate      string
	WhiteboardAppIdentifier  string
	WhiteboardAccessKey      string
	WhiteboardSecretKey      string
	WhiteboardRegion         string
	ClassroomDebugEarlyEntry bool
}

func Load() (Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("determine project directory: %w", err)
	}
	// Process environment keeps precedence; .env only supplies missing values.
	_ = loadEnvFile(filepath.Join(cwd, ".env"))
	cfg := Config{
		Addr:                    value("APP_ADDR", "127.0.0.1:8080"),
		MySQLHost:               value("MYSQL_HOST", "127.0.0.1"),
		MySQLPort:               value("MYSQL_PORT", "3306"),
		MySQLUser:               value("MYSQL_USER", "root"),
		MySQLPassword:           os.Getenv("MYSQL_PASSWORD"),
		MySQLDatabase:           value("MYSQL_DATABASE", "english"),
		ResourceRoot:            value("RESOURCE_ROOT", "storage/books"),
		WebRoot:                 value("WEB_ROOT", "web/dist"),
		GinMode:                 value("GIN_MODE", "release"),
		AppOrigin:               strings.TrimRight(value("APP_ORIGIN", "http://127.0.0.1:8080"), "/"),
		DevMailDir:              valueAllowEmpty("DEV_MAIL_DIR", ".local/mail"),
		SMTPHost:                strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:                value("SMTP_PORT", "587"),
		SMTPUser:                os.Getenv("SMTP_USER"),
		SMTPPassword:            os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:                strings.TrimSpace(os.Getenv("SMTP_FROM")),
		AdminEmail:              strings.TrimSpace(os.Getenv("ADMIN_EMAIL")),
		AdminPassword:           os.Getenv("ADMIN_PASSWORD"),
		EditorRoot:              value("EDITOR_STORAGE", "storage/editor"),
		Python:                  value("EDITOR_PYTHON", "python3"),
		AgoraAppID:              strings.TrimSpace(os.Getenv("AGORA_APP_ID")),
		AgoraAppCertificate:     strings.TrimSpace(os.Getenv("AGORA_APP_CERTIFICATE")),
		WhiteboardAppIdentifier: strings.TrimSpace(os.Getenv("AGORA_WHITEBOARD_APP_IDENTIFIER")),
		WhiteboardAccessKey:     strings.TrimSpace(os.Getenv("AGORA_WHITEBOARD_ACCESS_KEY")),
		WhiteboardSecretKey:     strings.TrimSpace(os.Getenv("AGORA_WHITEBOARD_SECRET_KEY")),
		WhiteboardRegion:        value("AGORA_WHITEBOARD_REGION", "sg"),
	}
	// This debugging override is deliberately available only when the server
	// listens on the local machine. It must never be enabled on a public bind.
	cfg.ClassroomDebugEarlyEntry = strings.EqualFold(strings.TrimSpace(os.Getenv("CLASSROOM_DEBUG_ALLOW_EARLY_ENTRY")), "true") && isLoopbackAddr(cfg.Addr)
	if strings.ContainsAny(cfg.MySQLDatabase, "`/\\\x00") || cfg.MySQLDatabase == "" {
		return Config{}, fmt.Errorf("MYSQL_DATABASE is invalid")
	}
	for target, ptr := range map[string]*string{"RESOURCE_ROOT": &cfg.ResourceRoot, "WEB_ROOT": &cfg.WebRoot, "DEV_MAIL_DIR": &cfg.DevMailDir, "EDITOR_STORAGE": &cfg.EditorRoot} {
		if *ptr == "" {
			continue
		}
		if !filepath.IsAbs(*ptr) {
			*ptr = filepath.Join(cwd, *ptr)
		}
		*ptr, err = filepath.Abs(*ptr)
		if err != nil {
			return Config{}, fmt.Errorf("resolve %s: %w", target, err)
		}
	}
	return cfg, nil
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		value := strings.TrimSpace(raw)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func (c Config) DSN() string {
	dsn := driver.NewConfig()
	dsn.User = c.MySQLUser
	dsn.Passwd = c.MySQLPassword
	dsn.Net = "tcp"
	dsn.Addr = c.MySQLHost + ":" + c.MySQLPort
	dsn.DBName = c.MySQLDatabase
	dsn.ParseTime = true
	dsn.Loc = nil
	dsn.Collation = "utf8mb4_unicode_ci"
	dsn.Params = map[string]string{"time_zone": "'+00:00'"}
	return dsn.FormatDSN()
}

func value(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}

func valueAllowEmpty(key, fallback string) string {
	if current, exists := os.LookupEnv(key); exists {
		return strings.TrimSpace(current)
	}
	return fallback
}
