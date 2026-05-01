package certwriter

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	gatewayv1alpha1 "github.com/jkaninda/goma-operator/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Writer fetches kubernetes.io/tls Secrets and writes cert/key files to disk.
type Writer struct {
	client    kubernetes.Interface
	namespace string
	baseDir   string

	mu     sync.Mutex
	hashes map[string]string // secretName -> sha256(cert+key)
}

// New creates a Writer that writes cert files under baseDir.
func New(cfg *rest.Config, namespace, baseDir string) (*Writer, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("certwriter: failed to create client: %w", err)
	}
	return &Writer{
		client:    cs,
		namespace: namespace,
		baseDir:   baseDir,
		hashes:    make(map[string]string),
	}, nil
}

func (w *Writer) Sync(ctx context.Context, routes []gatewayv1alpha1.Route) map[string]bool {
	needed := make(map[string]bool)
	for _, r := range routes {
		if r.Spec.TLS != nil && r.Spec.TLS.SecretName != "" {
			needed[r.Spec.TLS.SecretName] = true
		}
	}

	written := make(map[string]bool)

	for name := range needed {
		if err := w.syncSecret(ctx, name); err != nil {
			slog.Error("Failed to sync route TLS secret", "secret", name, "error", err)
		} else {
			written[name] = true
		}
	}

	// Clean up certs for secrets no longer referenced by any route.
	w.mu.Lock()
	for name := range w.hashes {
		if !needed[name] {
			dir := filepath.Join(w.baseDir, name)
			if err := os.RemoveAll(dir); err != nil {
				slog.Error("Failed to remove stale route cert dir", "dir", dir, "error", err)
			} else {
				slog.Info("Removed stale route cert", "secret", name)
			}
			delete(w.hashes, name)
		}
	}
	w.mu.Unlock()

	return written
}

func (w *Writer) syncSecret(ctx context.Context, name string) error {
	secret, err := w.client.CoreV1().Secrets(w.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get secret %s: %w", name, err)
	}

	cert, ok := secret.Data["tls.crt"]
	if !ok || len(cert) == 0 {
		return fmt.Errorf("secret %s has no tls.crt data", name)
	}
	key, ok := secret.Data["tls.key"]
	if !ok || len(key) == 0 {
		return fmt.Errorf("secret %s has no tls.key data", name)
	}

	// Skip write if unchanged.
	combined := append(cert, key...)
	hash := fmt.Sprintf("%x", sha256.Sum256(combined))

	w.mu.Lock()
	if w.hashes[name] == hash {
		w.mu.Unlock()
		slog.Debug("Route TLS cert unchanged", "secret", name)
		return nil
	}
	w.mu.Unlock()

	dir := filepath.Join(w.baseDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create dir %s: %w", dir, err)
	}

	if err := atomicWrite(filepath.Join(dir, "tls.crt"), cert); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(dir, "tls.key"), key); err != nil {
		return err
	}

	w.mu.Lock()
	w.hashes[name] = hash
	w.mu.Unlock()

	slog.Info("Wrote route TLS cert", "secret", name, "dir", dir)
	return nil
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
