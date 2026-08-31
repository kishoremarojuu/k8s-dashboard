package server

import "testing"

func TestAllowedHost(t *testing.T) {
	for _, host := range []string{"localhost:7777", "127.0.0.1:7777", "[::1]:7777"} {
		if !allowedHost(host) {
			t.Fatalf("expected %q to be allowed", host)
		}
	}
	for _, host := range []string{"example.com:7777", "0.0.0.0:7777", "192.168.1.10:7777"} {
		if allowedHost(host) {
			t.Fatalf("expected %q to be rejected", host)
		}
	}
}

func TestKubernetesAPIAllowlist(t *testing.T) {
	tests := []struct {
		method string
		path   string
		write  bool
		want   bool
	}{
		{"GET", "/api/v1/nodes", false, true},
		{"GET", "/api/v1/namespaces/default/pods/example/log", false, true},
		{"GET", "/api/v1/namespaces/default/secrets", false, false},
		{"GET", "/apis/apps/v1/namespaces/default/deployments", false, true},
		{"DELETE", "/api/v1/namespaces/default/pods/example", false, false},
		{"DELETE", "/api/v1/namespaces/default/pods/example", true, true},
		{"DELETE", "/api/v1/namespaces/default/secrets/example", true, false},
		{"POST", "/api/v1/namespaces/default/pods", true, false},
	}
	for _, test := range tests {
		if got := isAllowedKubernetesRequest(test.method, test.path, test.write); got != test.want {
			t.Errorf("isAllowedKubernetesRequest(%q, %q, %v) = %v, want %v", test.method, test.path, test.write, got, test.want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	if !sameOrigin("http://127.0.0.1:7777", "127.0.0.1:7777") {
		t.Fatal("same loopback origin should be allowed")
	}
	if sameOrigin("http://example.com:7777", "127.0.0.1:7777") {
		t.Fatal("cross-origin request should be rejected")
	}
}
