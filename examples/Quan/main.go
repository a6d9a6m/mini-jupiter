package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"mini-jupiter/examples/Quan/internal/coupon"
	"mini-jupiter/examples/Quan/internal/observability"
	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/task"
	"mini-jupiter/internal/middleware"
	"mini-jupiter/pkg/config"
	applog "mini-jupiter/pkg/log"
	"mini-jupiter/pkg/metric"
	"mini-jupiter/pkg/mysql"
	"mini-jupiter/pkg/redis"
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
	Log       applog.Config         `mapstructure:"log" yaml:"log"`
	Metric    metric.Config         `mapstructure:"metric" yaml:"metric"`
	Redis     redis.Config          `mapstructure:"redis" yaml:"redis"`
	MySQL     mysql.Config          `mapstructure:"mysql" yaml:"mysql"`
	Migration mysql.MigrationConfig `mapstructure:"migration" yaml:"migration"`
	Coupon    struct {
		Claim struct {
			IdempotencyTTL time.Duration `mapstructure:"idempotency_ttl" yaml:"idempotency_ttl"`
		} `mapstructure:"claim" yaml:"claim"`
	} `mapstructure:"coupon" yaml:"coupon"`
	Task struct {
		Queue        task.QueueConfig        `mapstructure:"queue" yaml:"queue"`
		Consume      task.ConsumeConfig      `mapstructure:"consume" yaml:"consume"`
		Compensation task.CompensationConfig `mapstructure:"compensation" yaml:"compensation"`
	} `mapstructure:"task" yaml:"task"`
	Outbox struct {
		Relay outbox.RelayConfig `mapstructure:"relay" yaml:"relay"`
	} `mapstructure:"outbox" yaml:"outbox"`
	Middleware struct {
		Recovery bool `mapstructure:"recovery" yaml:"recovery"`
		TraceID  bool `mapstructure:"trace_id" yaml:"trace_id"`
		Logging  bool `mapstructure:"logging" yaml:"logging"`
	} `mapstructure:"middleware" yaml:"middleware"`
}

func main() {
	var cfg AppConfig
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "examples/Quan/config.yaml"
	}
	if _, err := config.Load(configPath, &cfg); err != nil {
		panic(err)
	}
	if err := applog.Init(cfg.Log); err != nil {
		panic(err)
	}
	defer applog.Sync()

	mysqlComp, err := mysql.NewComponent(cfg.MySQL)
	if err != nil {
		applog.L(context.Background()).Fatal("mysql init failed", zap.Error(err))
	}
	migrationComp, err := mysql.NewMigrationComponent(mysqlComp.Client().Raw(), cfg.Migration)
	if err != nil {
		applog.L(context.Background()).Fatal("migration init failed", zap.Error(err))
	}

	txm, err := mysql.NewTxManager(mysqlComp.Client().Raw())
	if err != nil {
		applog.L(context.Background()).Fatal("tx manager init failed", zap.Error(err))
	}

	outboxRepo := outbox.NewRepository(mysqlComp.Client().Raw())
	taskRepo := task.NewRepository(mysqlComp.Client().Raw(), txm)

	var redisComp *redis.Component
	if cfg.Redis.Enabled {
		c, err := redis.NewComponent(cfg.Redis)
		if err != nil {
			applog.L(context.Background()).Fatal("redis init failed", zap.Error(err))
		}
		redisComp = c
	}

	repo := coupon.NewRepository(
		mysqlComp.Client().Raw(),
		txm,
		taskRepo,
		outboxRepo,
		cfg.Task.Consume.DefaultMaxRetry,
	)
	var redisClient *redis.Client
	if redisComp != nil {
		redisClient = redisComp.Client()
	}
	svc := coupon.NewService(repo, redisClient, cfg.Coupon.Claim.IdempotencyTTL)
	handler := coupon.NewHandler(svc)

	var (
		taskQueue      *task.Queue
		relayComp      *outbox.Relay
		consumer       *task.Consumer
		taskCompensate *task.Compensator
	)
	if redisClient != nil {
		q, err := task.NewQueue(redisClient, cfg.Task.Queue)
		if err != nil {
			applog.L(context.Background()).Fatal("task queue init failed", zap.Error(err))
		}
		taskQueue = q

		relay, err := outbox.NewRelay(outboxRepo, taskQueue, cfg.Outbox.Relay)
		if err != nil {
			applog.L(context.Background()).Fatal("outbox relay init failed", zap.Error(err))
		}
		relayComp = relay

		registry := task.NewHandlerRegistry()
		registry.Register(task.TaskTypeSendCouponNotice, task.NewSendCouponNoticeHandler())
		c, err := task.NewConsumer(taskRepo, taskQueue, registry, cfg.Task.Consume)
		if err != nil {
			applog.L(context.Background()).Fatal("task consumer init failed", zap.Error(err))
		}
		consumer = c

		compensator, err := task.NewCompensator(taskRepo, taskQueue, cfg.Task.Compensation)
		if err != nil {
			applog.L(context.Background()).Fatal("task compensator init failed", zap.Error(err))
		}
		taskCompensate = compensator
	}

	taskService := task.NewServiceWithQueue(txm, taskRepo, outboxRepo, taskQueue, cfg.Task.Consume.DefaultMaxRetry)
	taskHTTPHandler := task.NewHTTPHandler(taskService)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})
	var quanMetrics *observability.Metrics
	if cfg.Metric.Enabled {
		quanMetrics = observability.New(cfg.Metric.Namespace, nil, nil)
		metricPath := cfg.Metric.Path
		if metricPath == "" {
			metricPath = "/metrics"
		}
		mux.Handle(metricPath, quanMetrics.Handler())
	}
	handler.Register(mux)
	taskHTTPHandler.Register(mux)
	if quanMetrics != nil {
		if relayComp != nil {
			relayComp.SetMetrics(quanMetrics)
		}
		if consumer != nil {
			consumer.SetMetrics(quanMetrics)
		}
	}

	var middlewares []middleware.Middleware
	if cfg.Middleware.Recovery {
		middlewares = append(middlewares, middleware.Recovery())
	}
	if cfg.Middleware.TraceID {
		middlewares = append(middlewares, middleware.TraceID())
	}
	if cfg.Middleware.Logging {
		middlewares = append(middlewares, middleware.Logging(nil))
	}
	httpHandler := middleware.Chain(middlewares...)(mux)

	server := &http.Server{
		Addr:    cfg.HTTP.Addr,
		Handler: httpHandler,
	}

	app := runtime.NewWithOptions(runtime.WithStopTimeout(10 * time.Second))
	app.Use(mysqlComp, migrationComp)
	if redisComp != nil {
		app.Use(redisComp)
	}
	if relayComp != nil {
		app.Use(relayComp)
	}
	if consumer != nil {
		app.Use(consumer)
	}
	if taskCompensate != nil {
		app.Use(taskCompensate)
	}
	app.Use(&httpComponent{server: server})

	if err := app.Start(context.Background()); err != nil {
		applog.L(context.Background()).Fatal("app start failed", zap.Error(err))
	}
	applog.L(context.Background()).Info("quan server listening", zap.String("addr", cfg.HTTP.Addr))

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
