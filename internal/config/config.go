// Package config đọc cấu hình từ biến môi trường (12-factor). Mọi lỗi cấu hình được
// báo một lần, ngay lúc khởi động, thay vì nổ ra giữa chừng khi đang phục vụ request.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port      string
	ProjectID string
	Region    string
	Env       string // dev | prod

	FirestoreDB      string
	GeminiModel      string
	GeminiLocation   string
	AppCheckRequired bool

	ReceiptDailyLimit int
	ReadTimeout       time.Duration // ngân sách cho POST /receipts/read, dưới giới hạn 60 s của Hosting rewrite
	DefaultTimeout    time.Duration

	TasksQueue   string // projects/.../locations/.../queues/notify
	WorkerURL    string // URL nội bộ của mimi-worker
	TasksSAEmail string // danh tính OIDC khi Cloud Tasks gọi worker
	EmailAPIKey  string // gắn từ Secret Manager, chỉ worker và job cần
	EmailFrom    string
	TaxonomyKeys []string // nạp từ file nhúng trong binary, không từ env
}

func Load() (Config, error) {
	c := Config{
		Port:              get("PORT", "8080"),
		ProjectID:         os.Getenv("GOOGLE_CLOUD_PROJECT"),
		Region:            get("GOOGLE_CLOUD_REGION", "asia-southeast1"),
		Env:               get("MIMI_ENV", "dev"),
		FirestoreDB:       get("FIRESTORE_DATABASE", "(default)"),
		GeminiModel:       os.Getenv("GEMINI_MODEL"),
		GeminiLocation:    get("GEMINI_LOCATION", "asia-southeast1"),
		AppCheckRequired:  get("APP_CHECK_REQUIRED", "true") == "true",
		TasksQueue:        os.Getenv("TASKS_QUEUE"),
		WorkerURL:         os.Getenv("WORKER_URL"),
		TasksSAEmail:      os.Getenv("TASKS_SA_EMAIL"),
		EmailAPIKey:       os.Getenv("EMAIL_API_KEY"),
		EmailFrom:         get("EMAIL_FROM", "Mimi <hello@mimi.app>"),
		ReadTimeout:       55 * time.Second,
		DefaultTimeout:    10 * time.Second,
		ReceiptDailyLimit: 20,
	}
	var errs []error
	if c.ProjectID == "" {
		errs = append(errs, errors.New("GOOGLE_CLOUD_PROJECT is required"))
	}
	if v := os.Getenv("RECEIPT_DAILY_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			errs = append(errs, fmt.Errorf("RECEIPT_DAILY_LIMIT invalid: %q", v))
		}
		c.ReceiptDailyLimit = n
	}
	if c.Env == "prod" && !c.AppCheckRequired {
		errs = append(errs, errors.New("APP_CHECK_REQUIRED must be true in prod"))
	}
	return c, errors.Join(errs...)
}

// RequireAPI kiểm tra các biến chỉ mimi-api cần.
func (c Config) RequireAPI() error {
	var errs []error
	if c.GeminiModel == "" {
		errs = append(errs, errors.New("GEMINI_MODEL is required"))
	}
	if c.TasksQueue == "" || c.WorkerURL == "" || c.TasksSAEmail == "" {
		errs = append(errs, errors.New("TASKS_QUEUE, WORKER_URL, TASKS_SA_EMAIL are required"))
	}
	return errors.Join(errs...)
}

// RequireWorker kiểm tra các biến worker và job gửi email cần.
func (c Config) RequireWorker() error {
	if c.EmailAPIKey == "" {
		return errors.New("EMAIL_API_KEY is required (mount from Secret Manager)")
	}
	return nil
}

func get(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
