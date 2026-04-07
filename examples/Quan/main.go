package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	"mini-jupiter/examples/Quan/internal/adjudication/reservation"
	claimapi "mini-jupiter/examples/Quan/internal/api/claim"
	"mini-jupiter/examples/Quan/internal/claim"
	claimrequest "mini-jupiter/examples/Quan/internal/claim/request"
	"mini-jupiter/examples/Quan/internal/observability"
	"mini-jupiter/internal/middleware"
	"mini-jupiter/pkg/config"
	apperr "mini-jupiter/pkg/errors"
	applog "mini-jupiter/pkg/log"
	"mini-jupiter/pkg/metric"
	"mini-jupiter/pkg/mysql"
	"mini-jupiter/pkg/rabbitmq"
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
	RabbitMQ  rabbitmq.Config       `mapstructure:"rabbitmq" yaml:"rabbitmq"`
	MySQL     mysql.Config          `mapstructure:"mysql" yaml:"mysql"`
	Migration mysql.MigrationConfig `mapstructure:"migration" yaml:"migration"`
	Coupon    struct {
		Claim struct {
			IdempotencyTTL       time.Duration                           `mapstructure:"idempotency_ttl" yaml:"idempotency_ttl"`
			ReservationReconcile reservation.ReservationReconcilerConfig `mapstructure:"reservation_reconcile" yaml:"reservation_reconcile"`
			RequestMQ            claimrequest.RabbitMQConfig             `mapstructure:"request_mq" yaml:"request_mq"`
		} `mapstructure:"claim" yaml:"claim"`
	} `mapstructure:"coupon" yaml:"coupon"`
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
		configPath = "examples/Quan/config.sample.yaml"
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

	if !cfg.Redis.Enabled {
		applog.L(context.Background()).Fatal("claim request path requires redis to be enabled")
	}
	redisComp, err := redis.NewComponent(cfg.Redis)
	if err != nil {
		applog.L(context.Background()).Fatal("redis init failed", zap.Error(err))
	}

	if !cfg.RabbitMQ.Enabled {
		applog.L(context.Background()).Fatal("claim request path requires rabbitmq to be enabled")
	}
	rabbitComp, err := rabbitmq.NewComponent(cfg.RabbitMQ)
	if err != nil {
		applog.L(context.Background()).Fatal("rabbitmq init failed", zap.Error(err))
	}

	repo := claim.NewRepository(mysqlComp.Client().Raw(), txm)
	redisClient := redisComp.Client()
	rabbitClient := rabbitComp.Client()
	adjudicator := hotpath.NewAdjudicator(redisClient)

	requestStore, err := claimrequest.NewRedisRequestStore(redisClient, claimrequest.RequestStoreConfig{
		WaitReplicas:       1,
		WaitTimeout:        200 * time.Millisecond,
		SkipWaitOnStatuses: []claimrequest.Status{claimrequest.StatusEnqueued},
	})
	if err != nil {
		applog.L(context.Background()).Fatal("claim request store init failed", zap.Error(err))
	}
	requestPublisher, err := claimrequest.NewRabbitMQPublisher(rabbitClient, cfg.Coupon.Claim.RequestMQ)
	if err != nil {
		applog.L(context.Background()).Fatal("claim request publisher init failed", zap.Error(err))
	}

	admitter := claimrequest.NewRedisAdmitter(repo, adjudicator)
	acceptSvc := claimrequest.NewAcceptService(admitter, requestStore, requestPublisher)
	querySvc := claimrequest.NewQueryService(requestStore)
	claimHandler := claimapi.NewHandler(claimrequest.NewAppService(acceptSvc, querySvc))
	requestConsumer := claimrequest.NewConsumer(requestStore, claimrequest.NewSQLClaimWriter(repo), admitter)
	requestReconciler := claimrequest.NewReconciler(
		requestStore,
		requestPublisher,
		admitter,
		claimrequest.NewSQLClaimLookup(repo),
		claimrequest.ReconcilePolicy{},
	)

	requestConsumerComp, err := claimrequest.NewRabbitMQConsumerComponent(
		rabbitClient,
		requestConsumer,
		cfg.Coupon.Claim.RequestMQ,
	)
	if err != nil {
		applog.L(context.Background()).Fatal("claim request consumer init failed", zap.Error(err))
	}
	requestReconcilerComp, err := claimrequest.NewReconcilerComponent(
		requestReconciler,
		claimrequest.ReconcilerConfig{},
	)
	if err != nil {
		applog.L(context.Background()).Fatal("claim request reconciler init failed", zap.Error(err))
	}
	reservationReconciler, err := reservation.NewReservationReconciler(repo, adjudicator, cfg.Coupon.Claim.ReservationReconcile)
	if err != nil {
		applog.L(context.Background()).Fatal("coupon reservation reconciler init failed", zap.Error(err))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	})

	var httpMetrics *metric.Metrics
	if cfg.Metric.Enabled {
		httpMetrics = metric.New(cfg.Metric)
		quanMetrics := observability.New(cfg.Metric.Namespace, nil, nil)
		apperr.SetReporter(quanMetrics.ObserveAppError)
		metricPath := cfg.Metric.Path
		if metricPath == "" {
			metricPath = "/metrics"
		}
		mux.Handle(metricPath, quanMetrics.Handler())
		claimHandler.SetMetrics(quanMetrics)
		acceptSvc.SetMetrics(quanMetrics)
		requestPublisher.SetMetrics(quanMetrics)
		requestConsumer.SetMetrics(quanMetrics)
		requestReconciler.SetMetrics(quanMetrics)
	} else {
		apperr.SetReporter(nil)
	}
	claimHandler.Register(mux)

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
	app.Use(mysqlComp, migrationComp, redisComp, rabbitComp, requestConsumerComp, requestReconcilerComp, reservationReconciler, &httpComponent{server: server})

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
