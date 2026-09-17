package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w
	resultCh := make(chan struct {
		data string
		err  error
	})
	go func() {
		data, err := io.ReadAll(r)
		resultCh <- struct {
			data string
			err  error
		}{data: string(data), err: err}
	}()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("failed to close writer: %v", err)
	}
	os.Stdout = old
	result := <-resultCh
	if err := r.Close(); err != nil {
		t.Fatalf("failed to close reader: %v", err)
	}
	if result.err != nil {
		t.Fatalf("failed to read stdout: %v", result.err)
	}
	return result.data
}

func TestGetBalanceDialErrorIncludesEndpointAndCause(t *testing.T) {
	originalEndpoint := rpcEndpoint
	rpcEndpoint = "http://test.invalid"
	defer func() { rpcEndpoint = originalEndpoint }()

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 127.0.0.1:8080: connect: connection refused (test stub)")
	})}
	defer func() { http.DefaultClient = originalClient }()

	output := captureStdout(t, func() {
		getBalance("nhb1testaddress")
	})

	if !strings.Contains(output, "POST http://test.invalid") {
		t.Fatalf("expected output to include endpoint, got %q", output)
	}
	if !strings.Contains(output, "connection refused (test stub)") {
		t.Fatalf("expected output to include underlying error, got %q", output)
	}
}
