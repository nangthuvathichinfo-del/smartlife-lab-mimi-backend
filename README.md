# Mimi backend (Go)

Một binary, ba vai trò trên Cloud Run: `serve-api`, `serve-worker`, `job <name>`.
Thiết kế chi tiết: `docs/backend-design.md`. Hợp đồng API: `api/openapi.yaml`.

## Bắt đầu

```bash
# Đổi module path trong go.mod cho khớp repo thật, rồi:
make tidy          # tải cloud.google.com/go/firestore, firebase.google.com/go/v4, google.golang.org/genai
make test-domain   # các package chỉ dùng thư viện chuẩn, chạy được ngay cả khi chưa tidy
make test          # toàn bộ
make emulators     # cửa sổ khác: Firestore + Auth emulator
make run-api
```

## Triển khai

```bash
docker build -t asia-southeast1-docker.pkg.dev/$PROJECT/mimi/backend:$SHA .
gcloud run deploy mimi-api --image ... --args serve-api --service-account sa-api@... \
  --region asia-southeast1 --no-traffic --tag canary
gcloud run deploy mimi-worker --image ... --args serve-worker --ingress internal --no-allow-unauthenticated
gcloud run jobs deploy mimi-job-digest --image ... --args job,digest
firebase deploy --only firestore:rules,firestore:indexes
```
