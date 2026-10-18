// Package logx cấu hình slog ghi JSON theo định dạng Cloud Logging nhận diện được:
// "severity" thay cho "level", "message" thay cho "msg", và trace của request được gắn
// vào "logging.googleapis.com/trace" để log và Cloud Trace nối với nhau.
package logx

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey struct{}

// Trace là định danh trace của request hiện tại.
type Trace struct {
	ID     string // trace id (32 hex)
	SpanID string
}

func WithTrace(ctx context.Context, t Trace) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

func TraceFrom(ctx context.Context) (Trace, bool) {
	t, ok := ctx.Value(ctxKey{}).(Trace)
	return t, ok
}

// New tạo logger. projectID dùng để ghép tên trace đầy đủ.
func New(w io.Writer, projectID string, level slog.Leveler) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.LevelKey:
				a.Key = "severity"
				if lv, ok := a.Value.Any().(slog.Level); ok && lv == slog.LevelWarn {
					a.Value = slog.StringValue("WARNING")
				}
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
	})
	return slog.New(&traceHandler{Handler: h, project: projectID})
}

type traceHandler struct {
	slog.Handler
	project string
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if t, ok := TraceFrom(ctx); ok && t.ID != "" {
		r.AddAttrs(
			slog.String("logging.googleapis.com/trace", "projects/"+h.project+"/traces/"+t.ID),
			slog.String("logging.googleapis.com/spanId", t.SpanID),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithAttrs(as), project: h.project}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithGroup(name), project: h.project}
}
