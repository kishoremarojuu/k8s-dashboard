package kube

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

type Client struct {
	config         *rest.Config
	clientset      kubernetes.Interface
	proxy          *httputil.ReverseProxy
	currentContext string
	contexts       []string
}

type Health struct {
	Connected bool   `json:"connected"`
	Context   string `json:"context"`
	Version   string `json:"kubernetesVersion,omitempty"`
	Error     string `json:"error,omitempty"`
}

type NodeSummary struct {
	Name        string            `json:"name"`
	Ready       bool              `json:"ready"`
	Roles       []string          `json:"roles"`
	Version     string            `json:"version"`
	Capacity    map[string]string `json:"capacity"`
	Allocatable map[string]string `json:"allocatable"`
	CreatedAt   metav1.Time       `json:"createdAt"`
}

func New(kubeconfigPath, contextName string) (*Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}

	deferred := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	rawConfig, err := deferred.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	currentContext := rawConfig.CurrentContext
	if contextName != "" {
		currentContext = contextName
	}
	if currentContext == "" {
		return nil, fmt.Errorf("kubeconfig has no current context; pass --context")
	}

	restConfig, err := deferred.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build REST config for context %q: %w", currentContext, err)
	}
	restConfig.UserAgent = "kubernetes-troubleshooter/0.1"
	restConfig.QPS = 20
	restConfig.Burst = 40

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	proxy, err := newReverseProxy(restConfig)
	if err != nil {
		return nil, err
	}

	contexts := make([]string, 0, len(rawConfig.Contexts))
	for name := range rawConfig.Contexts {
		contexts = append(contexts, name)
	}
	sort.Strings(contexts)

	return &Client{
		config:         restConfig,
		clientset:      clientset,
		proxy:          proxy,
		currentContext: currentContext,
		contexts:       contexts,
	}, nil
}

func newReverseProxy(config *rest.Config) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(config.Host)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes API URL: %w", err)
	}
	transport, err := rest.TransportFor(config)
	if err != nil {
		return nil, fmt.Errorf("configure Kubernetes transport: %w", err)
	}

	return &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			request.URL.Scheme = target.Scheme
			request.URL.Host = target.Host
			request.Host = target.Host
			request.Header.Del("Origin")
			request.Header.Del("Referer")
		},
		Transport: transport,
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, proxyErr error) {
			http.Error(writer, "Kubernetes API request failed: "+proxyErr.Error(), http.StatusBadGateway)
		},
	}, nil
}

func (c *Client) CurrentContext() string { return c.currentContext }

func (c *Client) Contexts() []string { return append([]string(nil), c.contexts...) }

func (c *Client) Proxy() http.Handler { return c.proxy }

func (c *Client) Health(ctx context.Context) Health {
	health := Health{Context: c.currentContext}
	version, err := c.clientset.Discovery().ServerVersion()
	if err != nil {
		health.Error = err.Error()
		return health
	}
	health.Connected = true
	health.Version = version.GitVersion
	return health
}

func (c *Client) Namespaces(ctx context.Context) ([]string, error) {
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, namespace := range list.Items {
		names = append(names, namespace.Name)
	}
	sort.Strings(names)
	return names, nil
}

func (c *Client) Nodes(ctx context.Context) ([]NodeSummary, error) {
	list, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	nodes := make([]NodeSummary, 0, len(list.Items))
	for _, node := range list.Items {
		roles := make([]string, 0, 2)
		for label := range node.Labels {
			const prefix = "node-role.kubernetes.io/"
			if strings.HasPrefix(label, prefix) {
				roles = append(roles, strings.TrimPrefix(label, prefix))
			}
		}
		sort.Strings(roles)
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				ready = condition.Status == corev1.ConditionTrue
				break
			}
		}
		nodes = append(nodes, NodeSummary{
			Name:        node.Name,
			Ready:       ready,
			Roles:       roles,
			Version:     node.Status.NodeInfo.KubeletVersion,
			Capacity:    resourceList(node.Status.Capacity),
			Allocatable: resourceList(node.Status.Allocatable),
			CreatedAt:   node.CreationTimestamp,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

func resourceList(resources corev1.ResourceList) map[string]string {
	result := make(map[string]string, len(resources))
	for name, quantity := range resources {
		result[string(name)] = quantity.String()
	}
	return result
}

func (c *Client) TailFile(ctx context.Context, namespace, pod, container, path string, lines int) (string, error) {
	request := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{
		Container: container,
		Command:   []string{"tail", "-n", strconv.Itoa(lines), "--", path},
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.config, http.MethodPost, request.URL())
	if err != nil {
		return "", fmt.Errorf("create exec stream: %w", err)
	}
	var stdout, stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return "", fmt.Errorf("tail %s: %w: %s", path, err, message)
		}
		return "", fmt.Errorf("tail %s: %w", path, err)
	}
	return stdout.String(), nil
}
