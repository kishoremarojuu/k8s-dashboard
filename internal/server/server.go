package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shivakishoremaroju/k8s-dashboard/internal/kube"
)

type Config struct {
	Version      string
	IndexHTML    []byte
	WriteEnabled bool
	FileLogPaths []string
}

type Server struct {
	kube           *kube.Client
	config         Config
	allowedLogPath map[string]struct{}
}

func New(kubeClient *kube.Client, config Config) *Server {
	if config.FileLogPaths == nil {
		config.FileLogPaths = []string{}
	}
	allowedLogPath := make(map[string]struct{}, len(config.FileLogPaths))
	for _, path := range config.FileLogPaths {
		allowedLogPath[path] = struct{}{}
	}
	return &Server{kube: kubeClient, config: config, allowedLogPath: allowedLogPath}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/k8s-dashboard.html", s.index)
	mux.HandleFunc("/favicon.ico", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/api/product/v1/health", s.health)
	mux.HandleFunc("/api/product/v1/config", s.productConfig)
	mux.HandleFunc("/api/product/v1/contexts", s.contexts)
	mux.HandleFunc("/api/product/v1/namespaces", s.namespaces)
	mux.HandleFunc("/api/product/v1/nodes", s.nodes)
	mux.HandleFunc("/api/product/v1/file-logs", s.fileLogs)
	mux.HandleFunc("/tail", s.fileLogs)
	mux.HandleFunc("/api/", s.kubernetesAPI)
	mux.HandleFunc("/apis/", s.kubernetesAPI)
	return securityHeaders(loopbackOnly(mux))
}

func (s *Server) index(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" && request.URL.Path != "/k8s-dashboard.html" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(s.config.IndexHTML)
}

func (s *Server) health(writer http.ResponseWriter, request *http.Request) {
	if !requireGet(writer, request) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	health := s.kube.Health(ctx)
	status := http.StatusOK
	if !health.Connected {
		status = http.StatusServiceUnavailable
	}
	writeJSON(writer, status, map[string]any{"toolVersion": s.config.Version, "cluster": health})
}

func (s *Server) productConfig(writer http.ResponseWriter, request *http.Request) {
	if !requireGet(writer, request) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"version":      s.config.Version,
		"context":      s.kube.CurrentContext(),
		"writeEnabled": s.config.WriteEnabled,
		"fileLogs": map[string]any{
			"enabled": len(s.config.FileLogPaths) > 0,
			"paths":   s.config.FileLogPaths,
		},
	})
}

func (s *Server) contexts(writer http.ResponseWriter, request *http.Request) {
	if !requireGet(writer, request) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"current": s.kube.CurrentContext(),
		"items":   s.kube.Contexts(),
	})
}

func (s *Server) namespaces(writer http.ResponseWriter, request *http.Request) {
	if !requireGet(writer, request) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	items, err := s.kube.Namespaces(ctx)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "list namespaces", err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) nodes(writer http.ResponseWriter, request *http.Request) {
	if !requireGet(writer, request) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	items, err := s.kube.Nodes(ctx)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "list nodes", err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) fileLogs(writer http.ResponseWriter, request *http.Request) {
	if !requireGet(writer, request) {
		return
	}
	query := request.URL.Query()
	namespace := query.Get("namespace")
	pod := query.Get("pod")
	container := query.Get("container")
	path := query.Get("file")
	if namespace == "" || pod == "" || container == "" || path == "" {
		http.Error(writer, "namespace, pod, container, and file are required", http.StatusBadRequest)
		return
	}
	if _, ok := s.allowedLogPath[path]; !ok {
		http.Error(writer, "file path is not enabled by the local operator", http.StatusForbidden)
		return
	}
	lines := 200
	if value := query.Get("lines"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			http.Error(writer, "lines must be an integer", http.StatusBadRequest)
			return
		}
		lines = parsed
	}
	if lines < 1 {
		lines = 1
	}
	if lines > 5000 {
		lines = 5000
	}

	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	content, err := s.kube.TailFile(ctx, namespace, pod, container, path, lines)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "tail file", err)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write([]byte(content))
}

var readOnlyKubernetesPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/api/v1/namespaces$`),
	regexp.MustCompile(`^/api/v1/nodes(?:/[^/]+)?$`),
	regexp.MustCompile(`^/api/v1/pods$`),
	regexp.MustCompile(`^/api/v1/namespaces/[^/]+/pods(?:/[^/]+(?:/log)?)?$`),
	regexp.MustCompile(`^/api/v1/namespaces/[^/]+/events$`),
	regexp.MustCompile(`^/apis/apps/v1/namespaces/[^/]+/(?:deployments|daemonsets|replicasets)(?:/[^/]+)?$`),
	regexp.MustCompile(`^/apis/metrics\.k8s\.io/v1beta1/(?:nodes|pods)(?:/[^/]+)?$`),
}

var writableKubernetesPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/api/v1/namespaces/[^/]+/pods/[^/]+$`),
	regexp.MustCompile(`^/apis/apps/v1/namespaces/[^/]+/(?:deployments|daemonsets)/[^/]+$`),
}

func (s *Server) kubernetesAPI(writer http.ResponseWriter, request *http.Request) {
	if !isAllowedKubernetesRequest(request.Method, request.URL.Path, s.config.WriteEnabled) {
		if request.Method == http.MethodDelete && !s.config.WriteEnabled {
			http.Error(writer, "write actions are disabled; restart with --write to enable the allowlisted actions", http.StatusForbidden)
			return
		}
		http.Error(writer, "Kubernetes API path is not exposed by this tool", http.StatusForbidden)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	s.kube.Proxy().ServeHTTP(writer, request)
}

func isAllowedKubernetesRequest(method, path string, writeEnabled bool) bool {
	patterns := readOnlyKubernetesPaths
	if method == http.MethodDelete && writeEnabled {
		patterns = writableKubernetesPaths
	} else if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	for _, pattern := range patterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func requireGet(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	writer.Header().Set("Allow", "GET, HEAD")
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, operation string, err error) {
	writeJSON(writer, status, map[string]string{
		"error":   http.StatusText(status),
		"message": fmt.Sprintf("%s: %v", operation, err),
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !allowedHost(request.Host) {
			http.Error(writer, "invalid Host header", http.StatusForbidden)
			return
		}
		if origin := request.Header.Get("Origin"); origin != "" && !sameOrigin(origin, request.Host) {
			http.Error(writer, "cross-origin request blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func allowedHost(value string) bool {
	host := value
	if parsedHost, _, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return strings.EqualFold(parsed.Host, requestHost)
}
