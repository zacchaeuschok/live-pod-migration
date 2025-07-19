package agent

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pb "my.domain/guestbook/api/proto"
)

const (
	agentPort      = 50051
	maxMessageSize = 100 * 1024 * 1024 // 100MB
)

// Client provides methods to communicate with checkpoint agents on nodes
type Client struct {
	k8sClient client.Client
}

// NewClient creates a new agent client
func NewClient(k8sClient client.Client) *Client {
	return &Client{
		k8sClient: k8sClient,
	}
}

// CheckpointContainer performs a checkpoint operation on a container
func (c *Client) CheckpointContainer(ctx context.Context, nodeName, podNamespace, podName, containerName, podUID string) (string, error) {
	// Create gRPC connection to agent
	conn, err := c.dialAgent(ctx, nodeName)
	if err != nil {
		return "", fmt.Errorf("failed to connect to agent on node %s: %w", nodeName, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			// Log error but don't fail the operation
		}
	}()

	// Create checkpoint service client
	checkpointClient := pb.NewCheckpointServiceClient(conn)

	// Perform checkpoint
	req := &pb.CheckpointRequest{
		PodNamespace:  podNamespace,
		PodName:       podName,
		ContainerName: containerName,
		PodUid:        podUID,
	}

	resp, err := checkpointClient.Checkpoint(ctx, req)
	if err != nil {
		return "", fmt.Errorf("checkpoint RPC failed: %w", err)
	}

	if !resp.Success {
		return "", fmt.Errorf("checkpoint failed: %s", resp.Error)
	}

	return resp.ArtifactUri, nil
}

// RestoreContainer performs a restore operation on a container
func (c *Client) RestoreContainer(ctx context.Context, nodeName, artifactURI, podNamespace, podName, containerName, podUID string) error {
	// Create gRPC connection to agent
	conn, err := c.dialAgent(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("failed to connect to agent on node %s: %w", nodeName, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			// Log error but don't fail the operation
		}
	}()

	// Create checkpoint service client
	checkpointClient := pb.NewCheckpointServiceClient(conn)

	// Perform restore
	req := &pb.RestoreRequest{
		ArtifactUri:   artifactURI,
		PodNamespace:  podNamespace,
		PodName:       podName,
		ContainerName: containerName,
		PodUid:        podUID,
	}

	resp, err := checkpointClient.Restore(ctx, req)
	if err != nil {
		return fmt.Errorf("restore RPC failed: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("restore failed: %s", resp.Error)
	}

	return nil
}

// getNodeEndpoint gets the agent endpoint using node IP
func (c *Client) getNodeEndpoint(ctx context.Context, nodeName string) (string, error) {
	node := &corev1.Node{}
	if err := c.k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return "", fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return fmt.Sprintf("%s:%d", addr.Address, agentPort), nil
		}
	}

	return "", fmt.Errorf("no internal IP found for node %s", nodeName)
}

// dialAgent creates a gRPC connection to the agent on the specified node
func (c *Client) dialAgent(ctx context.Context, nodeName string) (*grpc.ClientConn, error) {
	endpoint, err := c.getNodeEndpoint(ctx, nodeName)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMessageSize),
			grpc.MaxCallSendMsgSize(maxMessageSize),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", endpoint, err)
	}

	return conn, nil
}
