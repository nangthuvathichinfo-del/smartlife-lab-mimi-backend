// Command mimi là binary duy nhất của backend Mimi, chạy một trong ba vai trò:
//
//	mimi serve-api      Cloud Run service mimi-api (ingress: all, sau Firebase Hosting)
//	mimi serve-worker   Cloud Run service mimi-worker (ingress: internal, nhận Cloud Tasks)
//	mimi job <name>     Cloud Run job do Cloud Scheduler gọi: digest | price-sync | fx-sync | cleanup
//
// main chỉ đọc cấu hình, nối phụ thuộc bằng constructor và chạy. Logic nằm trong internal/.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // image distroless/static không có tzdata hệ thống

	"cloud.google.com/go/firestore"

	"github.com/smartlife-lab/mimi/backend/internal/config"
	"github.com/smartlife-lab/mimi/backend/internal/gcp/fbauth"
	"github.com/smartlife-lab/mimi/backend/internal/gcp/fsstore"
	"github.com/smartlife-lab/mimi/backend/internal/gcp/gemini"
	"github.com/smartlife-lab/mimi/backend/internal/home"
	"github.com/smartlife-lab/mimi/backend/internal/httpapi"
	"github.com/smartlife-lab/mimi/backend/internal/inbox"
	"github.com/smartlife-lab/mimi/backend/internal/item"
	"github.com/smartlife-lab/mimi/backend/internal/notify"
	"github.com/smartlife-lab/mimi/backend/internal/plan"
	"github.com/smartlife-lab/mimi/backend/internal/platform/logx"
	"github.com/smartlife-lab/mimi/backend/internal/receipt"
	"github.com/smartlife-lab/mimi/backend/internal/spending"
	"github.com/smartlife-lab/mimi/backend/internal/user"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mimi:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mimi serve-api | serve-worker | job <name>")
	}
	// Cloud Run gửi SIGTERM trước khi dừng instance và cho khoảng 10 giây để dọn dẹp.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := logx.New(os.Stdout, cfg.ProjectID, slog.LevelInfo)

	fs, err := firestore.NewClientWithDatabase(ctx, cfg.ProjectID, cfg.FirestoreDB)
	if err != nil {
		return fmt.Errorf("firestore: %w", err)
	}
	defer fs.Close()
	store := fsstore.New(fs)

	switch args[0] {
	case "serve-api":
		return serveAPI(ctx, cfg, log, store)
	case "serve-worker":
		return serveWorker(ctx, cfg, log)
	case "job":
		if len(args) < 2 {
			return errors.New("usage: mimi job <digest|price-sync|fx-sync|cleanup>")
		}
		return runJob(ctx, args[1], cfg, log, store)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func serveAPI(ctx context.Context, cfg config.Config, log *slog.Logger, store *fsstore.Store) error {
	if err := cfg.RequireAPI(); err != nil {
		return err
	}
	auth, err := fbauth.New(ctx, cfg.ProjectID, cfg.AppCheckRequired)
	if err != nil {
		return err
	}
	taxonomy := loadTaxonomy()
	reader, err := gemini.New(ctx, cfg.ProjectID, cfg.GeminiLocation, cfg.GeminiModel, taxonomy)
	if err != nil {
		return err
	}
	receipts, err := receipt.New(reader, store, store, store, store, log,
		receipt.WithDailyLimit(cfg.ReceiptDailyLimit),
		receipt.WithTaxonomy(taxonomy))
	if err != nil {
		return err
	}
	var appCheck httpapi.AppCheckVerifier
	if cfg.AppCheckRequired {
		appCheck = auth
	}
	items := item.New(store, nil)
	planSvc := plan.New(store, store, nil)
	api, err := httpapi.New(httpapi.Deps{
		Log: log, Tokens: auth, AppCheck: appCheck, Principals: store, Profiles: store,
		Receipts: receipts, Items: items, Ready: store,
		Home:     home.New(store, items, planSvc, nil),
		Plan:     planSvc,
		Notes:    inbox.New(items, planSvc, nil),
		Spending: spending.New(store),
		Settings: user.NewSettingsService(store, nil),
	}, httpapi.WithTimeouts(cfg.ReadTimeout, cfg.DefaultTimeout))
	if err != nil {
		return err
	}
	return listen(ctx, log, cfg.Port, api.Handler())
}

// serveWorker: nhận task từ Cloud Tasks. Cloud Run đã kiểm tra OIDC (ingress internal,
// bắt buộc IAM), handler chỉ kiểm tra nội dung task.
func serveWorker(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /tasks/notify", func(w http.ResponseWriter, r *http.Request) {
		// Bước tiếp theo: giải mã task, kiểm tra uid và loại task trong danh sách cho phép,
		// gửi email qua Mailer với timeout. Trả 2xx khi xong hoặc lỗi vĩnh viễn, 5xx để
		// Cloud Tasks retry khi lỗi tạm thời.
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
	return listen(ctx, log, cfg.Port, mux)
}

func runJob(ctx context.Context, name string, cfg config.Config, log *slog.Logger, store *fsstore.Store) error {
	start := time.Now()
	var err error
	switch name {
	case "digest":
		auth, aerr := fbauth.New(ctx, cfg.ProjectID, false)
		if aerr != nil {
			return aerr
		}
		mailer, merr := newMailer(cfg, log)
		if merr != nil {
			return merr
		}
		_, err = notify.NewDigestJob(fsstore.NewNotifyStore(store, auth), mailer, log, nil).Run(ctx)
	case "price-sync", "fx-sync", "cleanup":
		err = fmt.Errorf("job %s: not implemented yet", name)
	default:
		err = fmt.Errorf("unknown job %q", name)
	}
	// Một dòng log có cấu trúc cho mỗi lượt chạy; metric dựa trên log báo động khi
	// "lần thành công gần nhất" quá cũ.
	log.InfoContext(ctx, "job finished", "job", name, "ok", err == nil, "duration_ms", time.Since(start).Milliseconds())
	return err
}

// newMailer: chưa chọn nhà cung cấp email. Ở dev ghi log (không nội dung); ở prod từ
// chối khởi động thay vì âm thầm không gửi.
func newMailer(cfg config.Config, log *slog.Logger) (notify.Mailer, error) {
	if cfg.Env == "prod" {
		if err := cfg.RequireWorker(); err != nil {
			return nil, err
		}
		return nil, errors.New("email provider not configured")
	}
	return logMailer{log: log}, nil
}

type logMailer struct{ log *slog.Logger }

func (m logMailer) Send(ctx context.Context, _, subject string, lines []string) error {
	m.log.InfoContext(ctx, "dev mail", "subject", subject, "lines", len(lines))
	return nil
}

// listen chạy HTTP server với timeout đầy đủ và tắt êm khi nhận SIGTERM.
func listen(ctx context.Context, log *slog.Logger, port string, h http.Handler) error {
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second, // tải ảnh hóa đơn trên mạng di động chậm
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	log.Info("shutting down")
	return srv.Shutdown(shutdownCtx)
}

// loadTaxonomy: KHÓA TẠM do khung dự án đặt cho đủ 27 danh mục. Thay bằng taxonomy thật
// trong gói mimi_taxonomy của spec, nhúng từ taxonomy.json bằng go:embed.
func loadTaxonomy() []string {
	return []string{
		"sua", "do-uong", "tuoi-song", "rau-cu", "trai-cay", "do-kho", "gia-vi", "do-dong-lanh",
		"banh-keo", "an-vat", "me-be", "hoa-pham", "ve-sinh", "my-pham", "giay", "do-dung-bep",
		"thu-cung", "thuoc", "van-phong-pham", "dien-gia-dung", "do-choi", "quan-ao", "giat-say",
		"nha-cua", "the-thao", "dich-vu", "khac",
	}
}
