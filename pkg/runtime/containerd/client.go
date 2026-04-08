package containerd

import (
	"context"
	"fmt"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
)

// Client wraps the containerd client for a specific namespace.
type Client struct {
	client    *containerd.Client
	namespace string
}

// NewClient creates a containerd client connected to the given socket.
func NewClient(socket, namespace string) (*Client, error) {
	c, err := containerd.New(socket)
	if err != nil {
		return nil, fmt.Errorf("connect to containerd at %s: %w", socket, err)
	}
	return &Client{client: c, namespace: namespace}, nil
}

// Close disconnects the client.
func (c *Client) Close() error {
	return c.client.Close()
}

// NamespacedContext returns a context with the containerd namespace set.
func (c *Client) NamespacedContext(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, c.namespace)
}

// Containerd returns the underlying containerd client.
func (c *Client) Containerd() *containerd.Client {
	return c.client
}

// Namespace returns the configured namespace.
func (c *Client) Namespace() string {
	return c.namespace
}
