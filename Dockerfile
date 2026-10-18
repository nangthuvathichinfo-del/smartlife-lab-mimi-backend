# Một image cho cả mimi-api, mimi-worker và các job; vai trò chọn bằng tham số dòng lệnh.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mimi ./cmd/mimi

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mimi /mimi
USER nonroot:nonroot
ENTRYPOINT ["/mimi"]
CMD ["serve-api"]
