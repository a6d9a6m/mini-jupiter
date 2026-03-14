package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"

	"mini-jupiter/examples/Quan/internal/coupon"
	"mini-jupiter/examples/Quan/internal/observability"
	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/task"
	"mini-jupiter/internal/middleware"
	"mini-jupiter/pkg/config"
	apperr "mini-jupiter/pkg/errors"
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
			IdempotencyTTL       time.Duration                      `mapstructure:"idempotency_ttl" yaml:"idempotency_ttl"`
			ReservationReconcile coupon.ReservationReconcilerConfig `mapstructure:"reservation_reconcile" yaml:"reservation_reconcile"`
			SideEffectDispatch   coupon.SideEffectDispatchConfig    `mapstructure:"side_effect_dispatch" yaml:"side_effect_dispatch"`
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
	taskConsumeReceiptRepo := task.NewConsumeReceiptRepository(mysqlComp.Client().Raw())
	sideEffectRepo := coupon.NewSideEffectRepository(mysqlComp.Client().Raw())

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
		sideEffectRepo,
	)
	var redisClient *redis.Client
	var adjudicator *coupon.Adjudicator
	if redisComp != nil {
		redisClient = redisComp.Client()
		adjudicator = coupon.NewAdjudicator(redisClient)
	}
	svc := coupon.NewServiceWithAdjudicator(repo, adjudicator, cfg.Coupon.Claim.IdempotencyTTL)
	handler := coupon.NewHandler(svc)

	var (
		taskQueue             *task.Queue
		relayComp             *outbox.Relay
		consumer              *task.Consumer
		taskCompensate        *task.Compensator
		reservationReconciler *coupon.ReservationReconciler
		sideEffectDispatcher  *coupon.SideEffectDispatcher
	)
	if redisClient != nil {
		reconciler, err := coupon.NewReservationReconciler(repo, adjudicator, cfg.Coupon.Claim.ReservationReconcile)
		if err != nil {
			applog.L(context.Background()).Fatal("coupon reservation reconciler init failed", zap.Error(err))
		}
		reservationReconciler = reconciler

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
		registry.Register(task.TaskTypeSendCouponNotice, task.NewSendCouponNoticeHandler(taskConsumeReceiptRepo))
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

	dispatcher, err := coupon.NewSideEffectDispatcher(sideEffectRepo, taskRepo, outboxRepo, cfg.Coupon.Claim.SideEffectDispatch)
	if err != nil {
		applog.L(context.Background()).Fatal("coupon side effect dispatcher init failed", zap.Error(err))
	}
	sideEffectDispatcher = dispatcher

	taskService := task.NewServiceWithQueue(txm, taskRepo, outboxRepo, taskQueue, cfg.Task.Consume.DefaultMaxRetry)
	taskHTTPHandler := task.NewHTTPHandler(taskService)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})
	var (
		quanMetrics *observability.Metrics
		httpMetrics *metric.Metrics
	)
	if cfg.Metric.Enabled {
		httpMetrics = metric.New(cfg.Metric)
		quanMetrics = observability.New(cfg.Metric.Namespace, nil, nil)
		apperr.SetReporter(quanMetrics.ObserveAppError)
		metricPath := cfg.Metric.Path
		if metricPath == "" {
			metricPath = "/metrics"
		}
		mux.Handle(metricPath, quanMetrics.Handler())
		handler.SetMetrics(quanMetrics)
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
		if taskCompensate != nil {
			taskCompensate.SetMetrics(quanMetrics)
		}
	} else {
		apperr.SetReporter(nil)
	}

	var middlewares []middleware.Middleware
	if cfg.Middleware.Recovery {
		middlewares = append(middlewares, middleware.Recovery())
	}
	if cfg.Middleware.TraceID {
		middlewares = append(middlewares, middleware.TraceID())
	}
	if cfg.Middleware.Logging {
		middlewares = append(middlewares, middleware.Logging(httpMetrics))
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
	if reservationReconciler != nil {
		app.Use(reservationReconciler)
	}
	if sideEffectDispatcher != nil {
		app.Use(sideEffectDispatcher)
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
	server   *http.Server
	listener net.Listener
}

func (h *httpComponent) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", h.server.Addr)
	if err != nil {
		return err
	}
	h.listener = ln
	go func() {
		if err := h.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			applog.L(context.Background()).Error("http server error", zap.Error(err))
		}
	}()
	return nil
}

func (h *httpComponent) Stop(ctx context.Context) error {
	return h.server.Shutdown(ctx)
}
