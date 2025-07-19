package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"k8s.io/apimachinery/pkg/util/wait"

	pb "my.domain/guestbook/api/proto"
)

const (
	port                     = ":50051"
	checkpointDir            = "/var/lib/kubelet/checkpoints"
	maxMessageSize           = 100 * 1024 * 1024 // 100MB
	checkpointTimeout        = 30 * time.Second
	checkpointBackoffSteps   = 5
	checkpointBackoffInitial = 2 * time.Second
	checkpointBackoffFactor  = 2.0
	
	// Kubelet certificate paths
	checkpointCertFile = "/etc/kubernetes/pki/apiserver-kubelet-client.crt"
	checkpointKeyFile  = "/etc/kubernetes/pki/apiserver-kubelet-client.key"
	checkpointCAFile   = "/var/lib/kubelet/pki/kubelet.crt"
)

// CheckpointServer implements the CheckpointService
type CheckpointServer struct {
	pb.UnimplementedCheckpointServiceServer
	nodeName string
}

// NewCheckpointServer creates a new checkpoint server
func NewCheckpointServer() *CheckpointServer {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		nodeName = "unknown"
	}
	
	return &CheckpointServer{
		nodeName: nodeName,
	}
}

// Checkpoint implements the checkpoint operation
func (s *CheckpointServer) Checkpoint(ctx context.Context, req *pb.CheckpointRequest) (*pb.CheckpointResponse, error) {
	log.Printf("Checkpoint request: namespace=%s, pod=%s, container=%s, uid=%s", 
		req.PodNamespace, req.PodName, req.ContainerName, req.PodUid)

	// Ensure checkpoint directory exists
	if err := os.MkdirAll(checkpointDir, 0755); err != nil {
		log.Printf("Failed to create checkpoint directory: %v", err)
		return &pb.CheckpointResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create checkpoint directory: %v", err),
		}, nil
	}

	// Create checkpoint using kubelet API
	url := fmt.Sprintf("https://%s:10250/checkpoint/%s/%s/%s",
		s.nodeName, req.PodNamespace, req.PodName, req.ContainerName)

	httpClient, err := s.makeTLSClient()
	if err != nil {
		log.Printf("Failed to create TLS client: %v", err)
		return &pb.CheckpointResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create TLS client: %v", err),
		}, nil
	}

	checkpointFiles, err := s.doCheckpointWithBackoff(ctx, httpClient, url)
	if err != nil {
		log.Printf("Failed to create checkpoint: %v", err)
		return &pb.CheckpointResponse{
			Success: false,
			Error:   fmt.Sprintf("checkpoint failed: %v", err),
		}, nil
	}

	if len(checkpointFiles) == 0 {
		return &pb.CheckpointResponse{
			Success: false,
			Error:   "no checkpoint files created",
		}, nil
	}

	// Return the first checkpoint file as the artifact URI
	artifactURI := fmt.Sprintf("file://%s", checkpointFiles[0])
	log.Printf("Checkpoint created successfully: %s", artifactURI)
	return &pb.CheckpointResponse{
		Success:     true,
		ArtifactUri: artifactURI,
		Message:     "checkpoint created successfully",
	}, nil
}

// Restore implements the restore operation
func (s *CheckpointServer) Restore(ctx context.Context, req *pb.RestoreRequest) (*pb.RestoreResponse, error) {
	log.Printf("Restore request: uri=%s, namespace=%s, pod=%s, container=%s", 
		req.ArtifactUri, req.PodNamespace, req.PodName, req.ContainerName)

	// For now, just validate the restore request
	// Full restore implementation would require coordination with kubelet/CRI
	if err := s.validateRestoreRequest(req); err != nil {
		log.Printf("Failed to validate restore request: %v", err)
		return &pb.RestoreResponse{
			Success: false,
			Error:   fmt.Sprintf("restore validation failed: %v", err),
		}, nil
	}

	log.Printf("Restore validation completed successfully")
	return &pb.RestoreResponse{
		Success: true,
		Message: "restore validation completed successfully",
	}, nil
}

// Health implements the health check
func (s *CheckpointServer) Health(_ context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{
		Healthy: true,
		Message: fmt.Sprintf("checkpoint agent healthy on node %s", s.nodeName),
	}, nil
}

// makeTLSClient creates an HTTP client with TLS configuration for kubelet
func (s *CheckpointServer) makeTLSClient() (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(checkpointCertFile, checkpointKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}

	caBytes, err := os.ReadFile(checkpointCAFile)
	if err != nil {
		// Try alternative CA path
		caBytes, err = os.ReadFile("/etc/kubernetes/pki/ca.crt")
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &http.Client{
		Timeout: checkpointTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates:       []tls.Certificate{cert},
				RootCAs:            pool,
				InsecureSkipVerify: true, // Skip verification due to IP SAN issues
			},
		},
	}, nil
}

// doCheckpointWithBackoff calls kubelet checkpoint API with exponential backoff
func (s *CheckpointServer) doCheckpointWithBackoff(ctx context.Context, httpClient *http.Client, url string) ([]string, error) {
	var checkpointFiles []string
	var lastErr error

	bo := wait.Backoff{
		Steps:    checkpointBackoffSteps,
		Duration: checkpointBackoffInitial,
		Factor:   checkpointBackoffFactor,
	}

	err := wait.ExponentialBackoff(bo, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			lastErr = fmt.Errorf("failed to create request: %w", err)
			return false, nil
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("kubelet request failed: %w", err)
			log.Printf("Kubelet request failed, retrying: %v", err)
			return false, nil
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Printf("Failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			data, _ := io.ReadAll(resp.Body)
			lastErr = fmt.Errorf("kubelet responded %d: %s", resp.StatusCode, string(data))
			log.Printf("Non-2xx from kubelet, retrying: %s", lastErr)
			return false, nil
		}

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			return false, nil
		}

		var parsed struct {
			Items []string `json:"items"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			lastErr = fmt.Errorf("failed to parse kubelet JSON response: %w", err)
			return false, nil
		}

		if len(parsed.Items) == 0 {
			lastErr = fmt.Errorf("no checkpoint files returned by kubelet")
			return false, nil
		}

		checkpointFiles = parsed.Items
		log.Printf("Checkpoint created successfully, files: %v", checkpointFiles)
		return true, nil
	})

	if err != nil {
		return nil, fmt.Errorf("checkpoint failed after retries: %w", lastErr)
	}

	return checkpointFiles, nil
}

// validateRestoreRequest validates a restore request
func (s *CheckpointServer) validateRestoreRequest(req *pb.RestoreRequest) error {
	if req.ArtifactUri == "" {
		return fmt.Errorf("artifact URI is required")
	}
	if req.PodNamespace == "" {
		return fmt.Errorf("pod namespace is required")
	}
	if req.PodName == "" {
		return fmt.Errorf("pod name is required")
	}
	if req.ContainerName == "" {
		return fmt.Errorf("container name is required")
	}
	
	// Validate artifact URI format
	if !filepath.IsAbs(req.ArtifactUri) && !strings.HasPrefix(req.ArtifactUri, "file://") {
		return fmt.Errorf("invalid artifact URI format")
	}
	
	return nil
}

func main() {
	log.Printf("Starting checkpoint agent on node %s", os.Getenv("NODE_NAME"))

	// Ensure checkpoint directory exists
	if err := os.MkdirAll(checkpointDir, 0755); err != nil {
		log.Fatalf("Failed to create checkpoint directory: %v", err)
	}

	// Create gRPC server
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	// Configure gRPC server with larger message size
	s := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessageSize),
		grpc.MaxSendMsgSize(maxMessageSize),
	)

	// Register services
	checkpointServer := NewCheckpointServer()
	pb.RegisterCheckpointServiceServer(s, checkpointServer)
	
	// Register health service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Enable reflection for debugging
	reflection.Register(s)

	// Handle graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		
		log.Println("Shutting down checkpoint agent...")
		s.GracefulStop()
	}()

	log.Printf("Checkpoint agent listening on %s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
