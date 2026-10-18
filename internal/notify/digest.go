// Package notify chứa job digest (Cloud Scheduler gọi mỗi 15 phút) và gửi thông báo.
// Bảo đảm: không gửi trong giờ yên lặng, mỗi ngày mỗi loại gộp tối đa một thư, chạy
// lại hay chạy chồng không gửi trùng (trừ trường hợp tiến trình chết đúng giữa lúc gửi
// và lúc đánh dấu: chấp nhận gửi ít nhất một lần cho thông báo).
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/smartlife-lab/mimi/backend/internal/domain"
)

// DueUser là người dùng có next_notify_at <= now (truy vấn có index, không quét toàn bộ).
type DueUser struct {
	UID      string
	Timezone string
	Email    string // lấy từ Firebase Auth lúc chạy, không lưu ở Firestore
	Name     string
}

type Content struct {
	Subject string
	Lines   []string // tên món sắp hết, khoản sắp đến hạn; không có giá
}

type Store interface {
	DueUsers(ctx context.Context, now time.Time, limit int) ([]DueUser, error)
	// BuildDigest trả nội dung; ok=false nếu không có gì đáng gửi.
	BuildDigest(ctx context.Context, uid string, today domain.Date) (Content, bool, error)
	// ClaimDigest tạo digests/{date}_{kind} nếu chưa có. false: đã gửi hoặc đang gửi.
	ClaimDigest(ctx context.Context, uid, id string) (bool, error)
	MarkDigest(ctx context.Context, uid, id, status string) error
	Reschedule(ctx context.Context, uid string, next time.Time) error
}

type Mailer interface {
	Send(ctx context.Context, to, subject string, lines []string) error
}

// ErrPermanent: nhà cung cấp email từ chối hẳn (địa chỉ hỏng). Không retry.
var ErrPermanent = errors.New("notify: permanent send failure")

type DigestJob struct {
	store       Store
	mail        Mailer
	log         *slog.Logger
	now         func() time.Time
	batch       int
	concurrency int
	sendTimeout time.Duration
}

func NewDigestJob(store Store, mail Mailer, log *slog.Logger, now func() time.Time) *DigestJob {
	if now == nil {
		now = time.Now
	}
	return &DigestJob{store: store, mail: mail, log: log, now: now, batch: 200, concurrency: 8, sendTimeout: 10 * time.Second}
}

type Result struct {
	Processed, Sent, Skipped, Failed int
}

// Run xử lý một lượt. Trả lỗi nếu không đọc được danh sách; lỗi từng người dùng chỉ
// được đếm và log để một hộp thư hỏng không chặn cả lượt.
func (j *DigestJob) Run(ctx context.Context) (Result, error) {
	now := j.now()
	users, err := j.store.DueUsers(ctx, now, j.batch)
	if err != nil {
		return Result{}, fmt.Errorf("digest: list due users: %w", err)
	}
	var (
		mu  sync.Mutex
		res Result
		wg  sync.WaitGroup
		sem = make(chan struct{}, j.concurrency) // giới hạn số thư gửi song song
	)
	for _, u := range users {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			outcome := j.one(ctx, u, now)
			mu.Lock()
			defer mu.Unlock()
			res.Processed++
			switch outcome {
			case "sent":
				res.Sent++
			case "skipped":
				res.Skipped++
			default:
				res.Failed++
			}
		}()
	}
	wg.Wait()
	j.log.InfoContext(ctx, "digest run", "processed", res.Processed, "sent", res.Sent, "skipped", res.Skipped, "failed", res.Failed)
	return res, nil
}

func (j *DigestJob) one(ctx context.Context, u DueUser, now time.Time) string {
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		loc = time.FixedZone("ICT", 7*3600)
	}
	if domain.InQuietHours(now, loc) {
		if err := j.store.Reschedule(ctx, u.UID, domain.NextAllowed(now, loc)); err != nil {
			j.log.ErrorContext(ctx, "reschedule failed", "err", err)
			return "failed"
		}
		return "skipped"
	}
	today := domain.Today(now, loc)
	// Lượt tiếp theo: 8h sáng mai theo giờ người dùng. Bản thật đọc settings.digest_time.
	next := today.AddDays(1).At(8, 0, loc)

	content, ok, err := j.store.BuildDigest(ctx, u.UID, today)
	if err != nil {
		j.log.ErrorContext(ctx, "build digest failed", "err", err)
		return "failed"
	}
	if !ok || u.Email == "" {
		_ = j.store.Reschedule(ctx, u.UID, next)
		return "skipped"
	}
	id := today.String() + "_daily"
	claimed, err := j.store.ClaimDigest(ctx, u.UID, id)
	if err != nil || !claimed {
		_ = j.store.Reschedule(ctx, u.UID, next)
		return "skipped"
	}
	sctx, cancel := context.WithTimeout(ctx, j.sendTimeout)
	defer cancel()
	if err := j.mail.Send(sctx, u.Email, content.Subject, content.Lines); err != nil {
		status := "retry"
		if errors.Is(err, ErrPermanent) {
			status = "bounced"
		}
		_ = j.store.MarkDigest(ctx, u.UID, id, status)
		j.log.WarnContext(ctx, "digest send failed", "status", status, "err", err)
		return "failed"
	}
	if err := j.store.MarkDigest(ctx, u.UID, id, "sent"); err != nil {
		j.log.ErrorContext(ctx, "mark digest failed", "err", err)
	}
	if err := j.store.Reschedule(ctx, u.UID, next); err != nil {
		j.log.ErrorContext(ctx, "reschedule failed", "err", err)
	}
	return "sent"
}
