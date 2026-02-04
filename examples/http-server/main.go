package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"mini-jupiter/pkg/config"
	apperr "mini-jupiter/pkg/errors"
	applog "mini-jupiter/pkg/log"
	"mini-jupiter/pkg/isolation"
	"mini-jupiter/pkg/metric"
	"mini-jupiter/internal/middleware"
	"mini-jupiter/pkg/pool"
	"mini-jupiter/pkg/ratelimiter"
	"mini-jupiter/pkg/runtime"

	"go.uber.org/zap"
)

type AppConfig struct {
	App struct {
		Name string `mapstructure:"name" yaml:"name"`
		Env  string `mapstructure:"env" yaml:"env"`
	} `mapstructure:"app" yaml:"app"`
	HTTP struct {
		Addr string `mapstructure:"addr" yaml:"addr"`
	} `mapstructure:"http" yaml:"http"`
	Log applog.Config `mapstructure:"log" yaml:"log"`
	Metric metric.Config `mapstructure:"metric" yaml:"metric"`
	RateLimit ratelimiter.Config `mapstructure:"ratelimit" yaml:"ratelimit"`
	Isolation isolation.Config `mapstructure:"isolation" yaml:"isolation"`
	Middleware struct {
		Recovery bool `mapstructure:"recovery" yaml:"recovery"`
		TraceID  bool `mapstructure:"trace_id" yaml:"trace_id"`
		Logging  bool `mapstructure:"logging" yaml:"logging"`
	} `mapstructure:"middleware" yaml:"middleware"`
}

func main() {
	//加载配置
	var cfg AppConfig
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "examples/http-server/config.yaml"
	}
	_, err := config.Load(
		configPath,
		&cfg,
		config.WithWatch(),
		config.WithOnChange(func(newCfg any) {
			c := newCfg.(*AppConfig)
			applog.L(context.Background()).Info("config reloaded",
				zap.String("app", c.App.Name),
				zap.String("env", c.App.Env),
				zap.String("addr", c.HTTP.Addr),
			)
		}),
	)
	//初始化日志
	if err != nil {
		panic(err)
	}
	if err := applog.Init(cfg.Log); err != nil {
		panic(err)
	}
	defer applog.Sync()

	//注册路由
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("slow ok"))
	})
	mux.HandleFunc("/panic", func(w http.ResponseWriter, _ *http.Request) {
		panic("panic from /panic")
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "method not allowed"))
			return
		}
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeNotFound, "user not found"))
	})
	wp := pool.New(4, pool.WithBuffer(128), pool.WithTaskTimeout(3*time.Second))
	mux.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "method not allowed"))
			return
		}
		if err := wp.Submit(r.Context(), func(ctx context.Context) error {
			select {
			case <-time.After(200 * time.Millisecond):
				applog.L(ctx).Info("job done")
				return nil
			case <-ctx.Done():
				applog.L(ctx).Warn("job canceled", zap.Error(ctx.Err()))
				return ctx.Err()
			}
		}); err != nil {
			apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeInternalError, "submit failed"))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("job accepted"))
	})

	var metrics *metric.Metrics
	if cfg.Metric.Enabled {
		metrics = metric.New(cfg.Metric)
		mux.Handle(cfg.Metric.Path, metrics.Handler())
		apperr.SetReporter(metrics.ObserveError)
	}

	var limiter *ratelimiter.Limiter
	if cfg.RateLimit.Enabled {
		limiter = ratelimiter.New(cfg.RateLimit.Rate, cfg.RateLimit.Burst)
	}
	var isoMgr *isolation.Manager
	if cfg.Isolation.Enabled {
		isoMgr = isolation.NewManager(cfg.Isolation)
	}

	var middlewares []middleware.Middleware
	if cfg.Middleware.Recovery {
		middlewares = append(middlewares, middleware.Recovery())
	}
	if cfg.Middleware.TraceID {
		middlewares = append(middlewares, middleware.TraceID())
	}
	if isoMgr != nil {
		middlewares = append(middlewares, middleware.Isolation(isoMgr))
	}
	if limiter != nil {
		middlewares = append(middlewares, middleware.RateLimit(limiter))
	}
	if cfg.Middleware.Logging {
		middlewares = append(middlewares, middleware.Logging(metrics))
	}
	handler := middleware.Chain(middlewares...)(mux)
	//创建sever
	server := &http.Server{
		Addr:    cfg.HTTP.Addr,
		Handler: handler,
	}
	//组件注册
	app := runtime.NewWithOptions(runtime.WithStopTimeout(8 * time.Second))
	app.Use(&httpComponent{server: server}, &poolComponent{pool: wp})
	//启动app
	if err := app.Start(context.Background()); err != nil {
		applog.L(context.Background()).Fatal("app start failed", zap.Error(err))
	}
	applog.L(context.Background()).Info("http server listening",
		zap.String("addr", cfg.HTTP.Addr),
	)

	_ = runtime.WaitSignal(context.Background())
	if err := app.Stop(context.Background()); err != nil {
		applog.L(context.Background()).Error("app stop failed", zap.Error(err))
	}
}

type httpComponent struct {
	server *http.Server
}

func (h *httpComponent) Start(_ context.Context) error {
	go func() {
		if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			applog.L(context.Background()).Error("http server error", zap.Error(err))
		}
	}()
	return nil
}

func (h *httpComponent) Stop(ctx context.Context) error {
	return h.server.Shutdown(ctx)
}

type poolComponent struct {
	pool *pool.Pool
}

func (p *poolComponent) Start(_ context.Context) error {
	return nil
}

func (p *poolComponent) Stop(_ context.Context) error {
	p.pool.Close()
	return nil
}
