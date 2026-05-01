package converter

import (
	"encoding/json"
	"fmt"
	"time"

	gatewayv1alpha1 "github.com/jkaninda/goma-operator/api/v1alpha1"
)

// ConfigBundle matches the structure expected by Goma's FileProvider.
type ConfigBundle struct {
	Version     string       `yaml:"version"`
	Routes      []Route      `yaml:"routes"`
	Middlewares []Middleware `yaml:"middlewares,omitempty"`
}

// Route represents a route in the ConfigBundle.
type Route struct {
	Name           string        `yaml:"name"`
	Path           string        `yaml:"path"`
	Rewrite        string        `yaml:"rewrite,omitempty"`
	Target         string        `yaml:"target,omitempty"`
	Methods        []string      `yaml:"methods,omitempty"`
	Hosts          []string      `yaml:"hosts,omitempty"`
	Backends       []Backend     `yaml:"backends,omitempty"`
	Priority       int           `yaml:"priority,omitempty"`
	Enabled        bool          `yaml:"enabled"`
	Middlewares    []string      `yaml:"middlewares,omitempty"`
	HealthCheck    *HealthCheck  `yaml:"healthCheck,omitempty"`
	Security       *Security     `yaml:"security,omitempty"`
	TLS            *RouteTLSCert `yaml:"tls,omitempty"`
	Maintenance    *Maintenance  `yaml:"maintenance,omitempty"`
	DisableMetrics bool          `yaml:"disableMetrics,omitempty"`
}

// RouteTLSCert is the per-route serving certificate.
type RouteTLSCert struct {
	Certificate RouteTLSCertPair `yaml:"certificate"`
}

// RouteTLSCertPair holds cert/key file paths.
type RouteTLSCertPair struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// Backend defines a backend server.
type Backend struct {
	Endpoint  string         `yaml:"endpoint"`
	Weight    int            `yaml:"weight,omitempty"`
	Match     []BackendMatch `yaml:"match,omitempty"`
	Exclusive bool           `yaml:"exclusive,omitempty"`
}

// BackendMatch is a request condition pinning traffic to a backend.
type BackendMatch struct {
	Source   string `yaml:"source"`
	Name     string `yaml:"name,omitempty"`
	Operator string `yaml:"operator"`
	Value    string `yaml:"value"`
}

// HealthCheck defines health check settings.
type HealthCheck struct {
	Path            string `yaml:"path,omitempty"`
	Interval        string `yaml:"interval,omitempty"`
	Timeout         string `yaml:"timeout,omitempty"`
	HealthyStatuses []int  `yaml:"healthyStatuses,omitempty"`
}

// Security defines route security settings.
type Security struct {
	ForwardHostHeaders      bool         `yaml:"forwardHostHeaders"`
	EnableExploitProtection bool         `yaml:"enableExploitProtection,omitempty"`
	TLS                     *SecurityTLS `yaml:"tls,omitempty"`
}

// SecurityTLS defines TLS settings.
type SecurityTLS struct {
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify,omitempty"`
	RootCAs            string `yaml:"rootCAs,omitempty"`
	ClientCert         string `yaml:"clientCert,omitempty"`
	ClientKey          string `yaml:"clientKey,omitempty"`
}

// Maintenance defines maintenance mode.
type Maintenance struct {
	Enabled bool   `yaml:"enabled"`
	Body    string `yaml:"body,omitempty"`
	Status  int    `yaml:"status,omitempty"`
}

// Middleware represents a middleware in the ConfigBundle.
type Middleware struct {
	Name  string      `yaml:"name"`
	Type  string      `yaml:"type"`
	Paths []string    `yaml:"paths,omitempty"`
	Rule  interface{} `yaml:"rule,omitempty"`
}

const certsBasePath = "/etc/goma/certs"

// RouteCertsBasePath is where the sidecar writes per-route TLS cert/key
// files fetched from Kubernetes Secrets.
const RouteCertsBasePath = "/etc/goma/route-certs"

// Converter transforms CRD types to ConfigBundle types.
type Converter struct{}

// New creates a new Converter.
func New() *Converter {
	return &Converter{}
}

// BuildBundle assembles a ConfigBundle from routes and middlewares.
func (c *Converter) BuildBundle(gatewayName string, routes []gatewayv1alpha1.Route, middlewares []gatewayv1alpha1.Middleware) ConfigBundle {
	bundle := ConfigBundle{
		Version:     fmt.Sprintf("k8s-%d", time.Now().Unix()),
		Routes:      make([]Route, 0, len(routes)),
		Middlewares: make([]Middleware, 0, len(middlewares)),
	}

	for _, r := range routes {
		bundle.Routes = append(bundle.Routes, c.routeFromCR(&r))
	}

	for _, m := range middlewares {
		bundle.Middlewares = append(bundle.Middlewares, c.middlewareFromCR(&m))
	}

	return bundle
}

func (c *Converter) routeFromCR(cr *gatewayv1alpha1.Route) Route {
	r := Route{
		Name:           cr.Name,
		Path:           cr.Spec.Path,
		Rewrite:        cr.Spec.Rewrite,
		Target:         cr.Spec.Target,
		Methods:        cr.Spec.Methods,
		Hosts:          cr.Spec.Hosts,
		Priority:       cr.Spec.Priority,
		Enabled:        cr.Spec.Enabled,
		Middlewares:    cr.Spec.Middlewares,
		DisableMetrics: cr.Spec.DisableMetrics,
	}

	if len(cr.Spec.Backends) > 0 {
		r.Backends = make([]Backend, 0, len(cr.Spec.Backends))
		for _, b := range cr.Spec.Backends {
			be := Backend{
				Endpoint:  b.Endpoint,
				Weight:    b.Weight,
				Exclusive: b.Exclusive,
			}
			if len(b.Match) > 0 {
				be.Match = make([]BackendMatch, 0, len(b.Match))
				for _, m := range b.Match {
					be.Match = append(be.Match, BackendMatch{
						Source:   m.Source,
						Name:     m.Name,
						Operator: m.Operator,
						Value:    m.Value,
					})
				}
			}
			r.Backends = append(r.Backends, be)
		}
	}

	if cr.Spec.HealthCheck != nil {
		r.HealthCheck = &HealthCheck{
			Path:            cr.Spec.HealthCheck.Path,
			Interval:        cr.Spec.HealthCheck.Interval,
			Timeout:         cr.Spec.HealthCheck.Timeout,
			HealthyStatuses: cr.Spec.HealthCheck.HealthyStatuses,
		}
	}

	if cr.Spec.Security != nil {
		sec := &Security{
			ForwardHostHeaders:      cr.Spec.Security.ForwardHostHeaders,
			EnableExploitProtection: cr.Spec.Security.EnableExploitProtection,
		}
		if cr.Spec.Security.TLS != nil {
			secTLS := &SecurityTLS{
				InsecureSkipVerify: cr.Spec.Security.TLS.InsecureSkipVerify,
			}
			if cr.Spec.Security.TLS.RootCAsSecret != "" {
				secTLS.RootCAs = fmt.Sprintf("%s/%s/ca.crt", certsBasePath, cr.Spec.Security.TLS.RootCAsSecret)
			}
			if cr.Spec.Security.TLS.ClientCertSecret != "" {
				secTLS.ClientCert = fmt.Sprintf("%s/%s/tls.crt", certsBasePath, cr.Spec.Security.TLS.ClientCertSecret)
				secTLS.ClientKey = fmt.Sprintf("%s/%s/tls.key", certsBasePath, cr.Spec.Security.TLS.ClientCertSecret)
			}
			sec.TLS = secTLS
		}
		r.Security = sec
	}

	// Per-route serving TLS — the sidecar writes the cert/key files fetched
	// from the Secret; here we just emit the expected file paths.
	if cr.Spec.TLS != nil && cr.Spec.TLS.SecretName != "" {
		r.TLS = &RouteTLSCert{
			Certificate: RouteTLSCertPair{
				Cert: fmt.Sprintf("%s/%s/tls.crt", RouteCertsBasePath, cr.Spec.TLS.SecretName),
				Key:  fmt.Sprintf("%s/%s/tls.key", RouteCertsBasePath, cr.Spec.TLS.SecretName),
			},
		}
	}

	if cr.Spec.Maintenance != nil {
		r.Maintenance = &Maintenance{
			Enabled: cr.Spec.Maintenance.Enabled,
			Body:    cr.Spec.Maintenance.Body,
			Status:  cr.Spec.Maintenance.Status,
		}
	}

	return r
}

func (c *Converter) middlewareFromCR(cr *gatewayv1alpha1.Middleware) Middleware {
	m := Middleware{
		Name:  cr.Name,
		Type:  cr.Spec.Type,
		Paths: cr.Spec.Paths,
	}

	if cr.Spec.Rule != nil && cr.Spec.Rule.Raw != nil {
		var rule interface{}
		if err := json.Unmarshal(cr.Spec.Rule.Raw, &rule); err == nil {
			m.Rule = rule
		}
	}

	return m
}
