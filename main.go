package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shivakishoremaroju/k8s-dashboard/internal/kube"
	"github.com/shivakishoremaroju/k8s-dashboard/internal/server"
)

const version = "0.1.0-dev"

//go:embed k8s-dashboard.html
var assets embed.FS

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func main() {
	var (
		address      = flag.String("address", "127.0.0.1:7777", "local address to listen on")
		kubeconfig   = flag.String("kubeconfig", "", "path to kubeconfig (defaults to standard loading rules)")
		contextName  = flag.String("context", "", "kubeconfig context to use (defaults to current-context)")
		writeEnabled = flag.Bool("write", false, "enable the small allowlist of destructive actions")
		noOpen       = flag.Bool("no-open", false, "do not open the dashboard in a browser")
		fileLogs     stringListFlag
	)
	flag.Var(&fileLogs, "file-log-path", "container file path that may be tailed; repeat for multiple paths")
	flag.Parse()

	if err := requireLoopbackAddress(*address); err != nil {
		log.Fatal(err)
	}

	indexHTML, err := assets.ReadFile("k8s-dashboard.html")
	if err != nil {
		log.Fatalf("read embedded dashboard: %v", err)
	}

	kubeClient, err := kube.New(*kubeconfig, *contextName)
	if err != nil {
		log.Fatalf("configure Kubernetes client: %v", err)
	}

	app := server.New(kubeClient, server.Config{
		Version:      version,
		IndexHTML:    indexHTML,
		WriteEnabled: *writeEnabled,
		FileLogPaths: fileLogs,
	})

	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatalf("listen on %s: %v", *address, err)
	}

	httpServer := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	dashboardURL := "http://" + listener.Addr().String()
	log.Printf("Kubernetes Troubleshooter %s", version)
	log.Printf("Context: %s", kubeClient.CurrentContext())
	log.Printf("Mode: %s", map[bool]string{true: "write-enabled", false: "read-only"}[*writeEnabled])
	log.Printf("Dashboard: %s", dashboardURL)

	if !*noOpen {
		go func() {
			time.Sleep(150 * time.Millisecond)
			if err := openBrowser(dashboardURL); err != nil {
				log.Printf("open browser: %v", err)
			}
		}()
	}

	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve dashboard: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("Shutting down")
	_ = httpServer.Close()
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid --address %q: %w", address, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--address must use a loopback host, got %q", host)
	}
	return nil
}

func openBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{url}
	case "linux":
		command, args = "xdg-open", []string{url}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported operating system %s", runtime.GOOS)
	}
	return exec.Command(command, args...).Start()
}
