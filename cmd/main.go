package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/goma-k8s-provider/internal/acme"
	"github.com/jkaninda/goma-k8s-provider/internal/certwriter"
	"github.com/jkaninda/goma-k8s-provider/internal/converter"
	"github.com/jkaninda/goma-k8s-provider/internal/watcher"
	"github.com/jkaninda/goma-k8s-provider/internal/writer"
	gatewayv1alpha1 "github.com/jkaninda/goma-operator/api/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(goutils.Env("GOMA_K8S_LOG_LEVEL", "info")),
	}))
	slog.SetDefault(logger)

	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))

	gatewayName := os.Getenv("GOMA_K8S_GATEWAY")
	if gatewayName == "" {
		slog.Error("GOMA_K8S_GATEWAY is required")
		os.Exit(1)
	}

	namespace := os.Getenv("GOMA_K8S_NAMESPACE")
	outputDir := goutils.Env("GOMA_K8S_OUTPUT_DIR", "/etc/goma/providers/k8s")
	debounceMs, _ := strconv.Atoi(goutils.Env("GOMA_K8S_DEBOUNCE_MS", "500"))
	acmeSecret := os.Getenv("GOMA_K8S_ACME_SECRET")
	acmeFile := goutils.Env("GOMA_K8S_ACME_FILE", "/etc/letsencrypt/acme.json")

	// Build kubernetes client config
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig for local development
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			slog.Error("Failed to build kubernetes config", "error", err)
			os.Exit(1)
		}
	}

	// Register CRD scheme
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gatewayv1alpha1.AddToScheme(scheme))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("Received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Setup writer
	w := writer.New(outputDir)

	// Setup converter
	conv := converter.New()

	// Setup ACME sync
	if acmeSecret != "" {
		acmeSync, err := acme.NewSync(cfg, namespace, acmeSecret, acmeFile)
		if err != nil {
			slog.Error("Failed to initialize ACME sync", "error", err)
			os.Exit(1)
		}

		// Restore ACME data from secret on startup
		if err := acmeSync.RestoreFromSecret(ctx); err != nil {
			slog.Warn("Failed to restore ACME data from secret (may not exist yet)", "error", err)
		}

		// Start watching acme.json for changes
		go func() {
			if err := acmeSync.WatchAndSync(ctx); err != nil {
				slog.Error("ACME sync failed", "error", err)
			}
		}()
	}

	cw, err := certwriter.New(cfg, namespace, converter.RouteCertsBasePath)
	if err != nil {
		slog.Error("Failed to initialize cert writer", "error", err)
		os.Exit(1)
	}

	// Setup watcher
	opts := watcher.Options{
		Config:      cfg,
		Scheme:      scheme,
		GatewayName: gatewayName,
		Namespace:   namespace,
		DebounceMs:  debounceMs,
		OnChange: func(routes []gatewayv1alpha1.Route, middlewares []gatewayv1alpha1.Middleware) {

			cw.Sync(ctx, routes)

			bundle := conv.BuildBundle(gatewayName, routes, middlewares)
			if err := w.Write(bundle); err != nil {
				slog.Error("Failed to write config", "error", err)
			}
		},
	}

	wt, err := watcher.New(opts)
	if err != nil {
		slog.Error("Failed to create watcher", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting goma-k8s-provider",
		"gateway", gatewayName,
		"namespace", namespace,
		"outputDir", outputDir,
	)

	if err := wt.Start(ctx); err != nil {
		slog.Error("Watcher stopped with error", "error", err)
		os.Exit(1)
	}
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
