.PHONY: tidy test test-domain lint vet emulators run-api build image

tidy:            ## tải module Google Cloud, Firebase, genai và sinh go.sum
	go mod tidy

test:            ## toàn bộ test (cần đã chạy tidy)
	go test -race ./...

test-domain:     ## test các package chỉ dùng thư viện chuẩn, chạy được ngay
	go test ./internal/domain/... ./internal/authz ./internal/receipt ./internal/item ./internal/httpapi ./internal/notify

vet:
	go vet ./...

lint:
	golangci-lint run

emulators:       ## Firestore + Auth emulator cho test tích hợp
	firebase emulators:start --only firestore,auth

run-api:         ## chạy API ở máy với emulator
	FIRESTORE_EMULATOR_HOST=127.0.0.1:8081 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099 \
	GOOGLE_CLOUD_PROJECT=mimi-dev APP_CHECK_REQUIRED=false GEMINI_MODEL=$${GEMINI_MODEL} \
	TASKS_QUEUE=local WORKER_URL=http://localhost:8090 TASKS_SA_EMAIL=local \
	go run ./cmd/mimi serve-api

build:
	CGO_ENABLED=0 go build -trimpath -o bin/mimi ./cmd/mimi

image:
	docker build -t mimi-backend .
