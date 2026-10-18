// Package fbauth bọc Firebase Admin SDK: xác minh ID token, App Check token và xóa
// tài khoản. Lưu ý: file này chưa được biên dịch trong môi trường soạn khung (không
// tải được module); kiểm tra lại chữ ký hàm với phiên bản SDK khi chạy go mod tidy.
package fbauth

import (
	"context"
	"fmt"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/appcheck"
	"firebase.google.com/go/v4/auth"
)

type Verifier struct {
	auth     *auth.Client
	appCheck *appcheck.Client // nil khi chạy emulator
}

// New dùng Application Default Credentials của service account Cloud Run (không key).
// Khi FIREBASE_AUTH_EMULATOR_HOST được đặt, SDK tự trỏ vào emulator.
func New(ctx context.Context, projectID string, withAppCheck bool) (*Verifier, error) {
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID})
	if err != nil {
		return nil, fmt.Errorf("fbauth: new app: %w", err)
	}
	a, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("fbauth: auth client: %w", err)
	}
	v := &Verifier{auth: a}
	if withAppCheck {
		ac, err := app.AppCheck(ctx)
		if err != nil {
			return nil, fmt.Errorf("fbauth: app check client: %w", err)
		}
		v.appCheck = ac
	}
	return v, nil
}

// VerifyIDToken kiểm tra chữ ký và hạn của token bằng khóa công khai (có cache), không
// gọi mạng mỗi request. Thao tác nhạy cảm (DELETE /me) nên gọi thêm kiểm tra thu hồi.
func (v *Verifier) VerifyIDToken(ctx context.Context, idToken string) (string, error) {
	t, err := v.auth.VerifyIDToken(ctx, idToken)
	if err != nil {
		return "", err
	}
	return t.UID, nil
}

func (v *Verifier) VerifyAppCheck(_ context.Context, token string) error {
	if v.appCheck == nil {
		return nil
	}
	_, err := v.appCheck.VerifyToken(token)
	return err
}

// Email trả email đăng nhập (chỉ nằm ở Firebase Auth) để job gửi thư.
func (v *Verifier) Email(ctx context.Context, uid string) (string, error) {
	u, err := v.auth.GetUser(ctx, uid)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}

// DeleteUser là bước cuối của DELETE /me, sau khi đã xóa hết dữ liệu Firestore.
func (v *Verifier) DeleteUser(ctx context.Context, uid string) error {
	err := v.auth.DeleteUser(ctx, uid)
	if auth.IsUserNotFound(err) {
		return nil // gọi lại sau lần lỗi trước: coi như đã xóa
	}
	return err
}
