package watcher

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	gatewayv1alpha1 "github.com/jkaninda/goma-operator/api/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Options configures the watcher.
type Options struct {
	Config      *rest.Config
	Scheme      *runtime.Scheme
	GatewayName string
	Namespace   string
	DebounceMs  int
	OnChange    func(routes []gatewayv1alpha1.Route, middlewares []gatewayv1alpha1.Middleware)
}

// Watcher watches Route and Middleware CRDs and triggers callbacks on changes.
type Watcher struct {
	opts Options

	cache       cache.Cache
	debounceDur time.Duration

	mu       sync.Mutex
	debounce *time.Timer
}

// New creates a new Watcher.
func New(opts Options) (*Watcher, error) {
	cacheOpts := cache.Options{
		Scheme: opts.Scheme,
	}
	if opts.Namespace != "" {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{
			opts.Namespace: {},
		}
	}

	c, err := cache.New(opts.Config, cacheOpts)
	if err != nil {
		return nil, err
	}

	debounceMs := opts.DebounceMs
	if debounceMs <= 0 {
		debounceMs = 500
	}

	return &Watcher{
		opts:        opts,
		cache:       c,
		debounceDur: time.Duration(debounceMs) * time.Millisecond,
	}, nil
}

// Start begins watching and blocks until the context is cancelled.
func (w *Watcher) Start(ctx context.Context) error {
	slog.Info("Watcher starting",
		"gateway", w.opts.GatewayName,
		"namespace", w.opts.Namespace,
		"debounce", w.debounceDur.String(),
	)

	// Register informers BEFORE starting the cache so the cache brings them up.
	routeInformer, err := w.cache.GetInformer(ctx, &gatewayv1alpha1.Route{})
	if err != nil {
		return err
	}
	mwInformer, err := w.cache.GetInformer(ctx, &gatewayv1alpha1.Middleware{})
	if err != nil {
		return err
	}

	handler := &eventHandler{watcher: w}
	if _, err := routeInformer.AddEventHandler(handler); err != nil {
		return err
	}
	if _, err := mwInformer.AddEventHandler(handler); err != nil {
		return err
	}

	// Start the cache in a goroutine — it blocks until ctx is cancelled.
	cacheErrCh := make(chan error, 1)
	go func() {
		cacheErrCh <- w.cache.Start(ctx)
	}()

	slog.Info("Waiting for cache sync...")
	syncCtx, cancelSync := context.WithTimeout(ctx, 60*time.Second)
	defer cancelSync()
	if !w.cache.WaitForCacheSync(syncCtx) {

		slog.Error("Cache sync failed or timed out — check RBAC and CRD installation",
			"gatewayCRDGroup", "gateway.jkaninda.dev",
			"namespace", w.opts.Namespace,
		)
		return context.DeadlineExceeded
	}
	slog.Info("Cache synced — performing initial config generation")
	w.triggerSync(ctx)

	// Block until the cache stops (ctx cancelled) or returns an error.
	for {
		select {
		case <-ctx.Done():
			slog.Info("Watcher stopping: context cancelled")
			return nil
		case err := <-cacheErrCh:
			if err != nil {
				slog.Error("Cache stopped with error", "error", err)
				return err
			}
			return nil
		}
	}
}

// triggerSync performs a full list of Routes and Middlewares and calls OnChange.
func (w *Watcher) triggerSync(ctx context.Context) {
	listOpts := []client.ListOption{}
	if w.opts.Namespace != "" {
		listOpts = append(listOpts, client.InNamespace(w.opts.Namespace))
	}

	routeList := &gatewayv1alpha1.RouteList{}
	if err := w.cache.List(ctx, routeList, listOpts...); err != nil {
		slog.Error("Failed to list routes", "error", err)
		return
	}

	// Filter routes that reference this gateway in their spec.gateways list.
	var routes []gatewayv1alpha1.Route
	referencedMW := make(map[string]bool)
	for _, route := range routeList.Items {
		for _, gwName := range route.Spec.Gateways {
			if gwName == w.opts.GatewayName {
				routes = append(routes, route)
				for _, mwName := range route.Spec.Middlewares {
					referencedMW[mwName] = true
				}
				break
			}
		}
	}

	// Sort routes by priority (desc) then name (asc) for deterministic output.
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Spec.Priority != routes[j].Spec.Priority {
			return routes[i].Spec.Priority > routes[j].Spec.Priority
		}
		return routes[i].Name < routes[j].Name
	})

	// List middlewares and keep only those referenced by the collected routes.
	mwList := &gatewayv1alpha1.MiddlewareList{}
	if err := w.cache.List(ctx, mwList, listOpts...); err != nil {
		slog.Error("Failed to list middlewares", "error", err)
		return
	}

	var middlewares []gatewayv1alpha1.Middleware
	for _, mw := range mwList.Items {
		if referencedMW[mw.Name] {
			middlewares = append(middlewares, mw)
		}
	}
	sort.Slice(middlewares, func(i, j int) bool {
		return middlewares[i].Name < middlewares[j].Name
	})

	slog.Info("Sync",
		"gateway", w.opts.GatewayName,
		"routesTotal", len(routeList.Items),
		"routesMatched", len(routes),
		"middlewaresTotal", len(mwList.Items),
		"middlewaresReferenced", len(middlewares),
	)

	w.opts.OnChange(routes, middlewares)
}

// scheduleSync coalesces multiple events within debounceDur into a single sync.
func (w *Watcher) scheduleSync() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.debounce != nil {
		w.debounce.Stop()
	}
	w.debounce = time.AfterFunc(w.debounceDur, func() {
		w.triggerSync(context.Background())
	})
}

// eventHandler implements cache.ResourceEventHandler
type eventHandler struct {
	watcher *Watcher
}

func (h *eventHandler) OnAdd(obj interface{}, _ bool) {
	slog.Debug("Event: Add", "type", typeOf(obj))
	h.watcher.scheduleSync()
}

func (h *eventHandler) OnUpdate(_, obj interface{}) {
	slog.Debug("Event: Update", "type", typeOf(obj))
	h.watcher.scheduleSync()
}

func (h *eventHandler) OnDelete(obj interface{}) {
	slog.Debug("Event: Delete", "type", typeOf(obj))
	h.watcher.scheduleSync()
}

func typeOf(obj interface{}) string {
	switch obj.(type) {
	case *gatewayv1alpha1.Route:
		return "Route"
	case *gatewayv1alpha1.Middleware:
		return "Middleware"
	default:
		return "unknown"
	}
}
