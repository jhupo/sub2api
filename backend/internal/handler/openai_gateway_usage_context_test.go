package handler

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageWorkerCallbacksDoNotCaptureGinContext(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Conservatively forbid these names inside worker callbacks, even
			// when shadowed: worker-local contexts must have distinct names.
			ginParams := make(map[string]bool)
			for _, param := range fn.Type.Params.List {
				ptr, ok := param.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				sel, ok := ptr.X.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Context" {
					continue
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "gin" {
					continue
				}
				for _, name := range param.Names {
					ginParams[name.Name] = true
				}
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "submit") || !strings.HasSuffix(sel.Sel.Name, "UsageRecordTask") {
					return true
				}
				for _, arg := range call.Args {
					callback, ok := arg.(*ast.FuncLit)
					if !ok {
						continue
					}
					checked++
					ast.Inspect(callback.Body, func(node ast.Node) bool {
						if id, ok := node.(*ast.Ident); ok && ginParams[id.Name] {
							t.Errorf("%s: usage worker captures pooled Gin context %q", fset.Position(id.Pos()), id.Name)
						}
						return true
					})
				}
				return true
			})
		}
	}
	require.GreaterOrEqual(t, checked, 10, "guard must inspect the real gateway callbacks")
}

func TestUsageFieldsSurviveGinContextReuse(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 8, TaskTimeout: time.Second,
	})
	t.Cleanup(pool.Stop)
	blocked, release := make(chan struct{}), make(chan struct{})
	pool.Submit(func(ctx context.Context) {
		close(blocked)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	<-blocked
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(context.WithValue(context.Background(), ctxkey.RequestedPublicModel, "public-model-user-A"))
	fields := clientRequestedUsageFields(c, service.ChannelMappingResult{BillingModelSource: service.BillingModelSourceRequested}, "mapped", "upstream")
	got := make(chan service.ChannelUsageFields, 1)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}
	h.submitUsageRecordTask(c.Request.Context(), func(context.Context) { got <- fields })
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(context.WithValue(context.Background(), ctxkey.RequestedPublicModel, "public-model-user-B"))
	close(release)
	select {
	case recorded := <-got:
		require.Equal(t, "public-model-user-A", recorded.OriginalModel)
	case <-time.After(time.Second):
		t.Fatal("usage task did not complete")
	}
}

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
}

func TestOpenAISubmitUsageRecordTaskSurvivesRequestCancellation(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.RequestID, "canceled-billing-request")
	parent, cancel := context.WithCancel(parent)
	cancel()
	h := &OpenAIGatewayHandler{}
	called := false
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		called = true
		require.NoError(t, ctx.Err(), "client cancellation must not cancel settlement")
		require.Equal(t, "canceled-billing-request", ctx.Value(ctxkey.RequestID))
		_, bounded := ctx.Deadline()
		require.True(t, bounded)
	})
	require.True(t, called)
}
