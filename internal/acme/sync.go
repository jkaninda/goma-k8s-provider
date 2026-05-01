package acme

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/fsnotify/fsnotify"
)

// Sync handles bidirectional sync between acme.json and a K8s Secret.
type Sync struct {
	client     kubernetes.Interface
	namespace  string
	secretName string
	filePath   string
}

// NewSync creates a new ACME Sync.
func NewSync(cfg *rest.Config, namespace, secretName, filePath string) (*Sync, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &Sync{
		client:     clientset,
		namespace:  namespace,
		secretName: secretName,
		filePath:   filePath,
	}, nil
}

// RestoreFromSecret reads the ACME Secret and writes its data to the acme.json file.
// Called on startup to restore certificates from a previous run.
func (s *Sync) RestoreFromSecret(ctx context.Context) error {
	secret, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, s.secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			slog.Info("ACME secret not found, starting fresh", "secret", s.secretName)
			return nil
		}
		return fmt.Errorf("failed to get ACME secret: %w", err)
	}

	data, ok := secret.Data["acme.json"]
	if !ok || len(data) == 0 {
		slog.Info("ACME secret exists but has no acme.json data")
		return nil
	}

	// Validate the payload is parsable JSON before overwriting any existing
	if !json.Valid(data) {
		return fmt.Errorf("ACME secret %q contains invalid JSON; refusing to restore", s.secretName)
	}

	// Ensure directory exists
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(s.filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write acme.json: %w", err)
	}

	slog.Info("Restored ACME data from secret", "secret", s.secretName, "bytes", len(data))
	return nil
}

// WatchAndSync watches the acme.json file for changes and syncs them to the K8s Secret.
func (s *Sync) WatchAndSync(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// Watch the directory (file may not exist yet)
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory %s: %w", dir, err)
	}

	slog.Info("Watching ACME file for changes", "path", s.filePath)

	var debounceTimer *time.Timer
	debounceDuration := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Only react to the acme.json file
			if filepath.Base(event.Name) != filepath.Base(s.filePath) {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Debounce writes
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(debounceDuration, func() {
				if err := s.syncToSecret(ctx); err != nil {
					slog.Error("Failed to sync ACME data to secret", "error", err)
				}
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Error("File watcher error", "error", err)
		}
	}
}

func (s *Sync) syncToSecret(ctx context.Context) error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read acme.json: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	if !json.Valid(data) {
		slog.Warn("acme.json is not valid JSON yet, skipping sync", "path", s.filePath)
		return nil
	}

	secret, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, s.secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Create new secret
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.secretName,
				Namespace: s.namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "goma-k8s-provider",
					"app.kubernetes.io/part-of":    "goma-gateway",
				},
			},
			Data: map[string][]byte{
				"acme.json": data,
			},
		}
		if _, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create ACME secret: %w", err)
		}
		slog.Info("Created ACME secret", "secret", s.secretName, "bytes", len(data))
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get ACME secret: %w", err)
	}

	// Update existing secret
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data["acme.json"] = data

	if _, err := s.client.CoreV1().Secrets(s.namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update ACME secret: %w", err)
	}

	slog.Info("Synced ACME data to secret", "secret", s.secretName, "bytes", len(data))
	return nil
}
