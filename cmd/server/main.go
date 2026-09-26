package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xiaov2/internal/config"
	"xiaov2/internal/database"
	"xiaov2/internal/handler"
	"xiaov2/internal/repository"
	"xiaov2/internal/resource"
	"xiaov2/internal/router"
	"xiaov2/internal/service"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("准备启动：前台 http://%s/ · 后台 http://%s/admin", cfg.Addr, cfg.Addr)
	db, err := database.Open(cfg)
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err = database.Migrate(db); err != nil {
		log.Fatal(err)
	}
	resources, err := resource.New(cfg.ResourceRoot)
	if err != nil {
		log.Fatal(err)
	}
	bookRepo := repository.NewBookRepository(db)
	bookService := service.NewBookService(bookRepo, resources)
	platformService := service.NewPlatformService(repository.NewPlatformRepository(db), cfg)
	classroomService := service.NewClassroomService(db, platformService)
	editorService := service.NewEditorService(db, cfg, resources)
	platformService.SetTTSModelSwitchHook(editorService.ReleaseAudioDaemonForModelSwitch)
	platformService.SetTranslationModelSwitchHook(editorService.ReleaseTranslationDaemonForModelSwitch)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "bootstrap":
			if err = platformService.Bootstrap(context.Background(), cfg.AdminEmail, cfg.AdminPassword); err != nil {
				log.Fatal(err)
			}
			log.Print("超级管理员初始化完成")
			return
		case "worker":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			if err = editorService.RunWorker(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Fatal(err)
			}
			return
		default:
			log.Fatal("unknown command; use bootstrap or worker")
		}
	}
	u, _ := url.Parse(cfg.AppOrigin)
	platformHandler := handler.NewPlatformHandler(platformService, u != nil && u.Scheme == "https")
	engine := router.New(db, handler.NewBookHandler(bookService), cfg.WebRoot, cfg.GinMode, platformHandler, handler.NewEditorHandler(editorService), handler.NewClassroomHandler(classroomService, cfg.EditorRoot), cfg.AppOrigin)
	server := &http.Server{Addr: cfg.Addr, Handler: engine, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 300 * time.Second, WriteTimeout: 310 * time.Second, IdleTimeout: 60 * time.Second}
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		if workerErr := editorService.RunWorker(stop); workerErr != nil && !errors.Is(workerErr, context.Canceled) {
			log.Printf("教材工作进程停止: %v", workerErr)
		}
	}()
	go classroomService.RunReconciler(stop)
	go func() {
		<-stop.Done()
		ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("xiaoV2 listening on http://%s", cfg.Addr)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
