package pod

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	pkg_file "github.com/leptonai/gpud/pkg/file"
	"github.com/leptonai/gpud/pkg/log"
)

const (
	defaultSocketFile               = "/run/containerd/containerd.sock"
	defaultContainerRuntimeEndpoint = "unix:///run/containerd/containerd.sock"
)

// NOTE
// DO NOT USE https://github.com/kubernetes/kubernetes/blob/v1.32.0-alpha.0/staging/src/k8s.io/cri-client/pkg/remote_runtime.go yet
// it fails with
// "code = Unavailable desc = name resolver error: produced zero addresses"

const (
	// maxMsgSize use 16MB as the default message size limit.
	// grpc library default is 4MB
	maxMsgSize = 1024 * 1024 * 16

	// connection parameters
	maxBackoffDelay      = 3 * time.Second
	baseBackoffDelay     = 100 * time.Millisecond
	minConnectionTimeout = 10 * time.Second
)

// ref. https://github.com/kubernetes/kubernetes/blob/v1.29.2/pkg/kubelet/cri/remote/remote_runtime.go
func defaultDialOptions() []grpc.DialOption {
	cps := grpc.ConnectParams{Backoff: backoff.DefaultConfig}
	cps.MinConnectTimeout = minConnectionTimeout
	cps.Backoff.BaseDelay = baseBackoffDelay
	cps.Backoff.MaxDelay = maxBackoffDelay
	return []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSize)),
		grpc.WithConnectParams(cps),
		grpc.WithContextDialer(dialUnix),
		grpc.WithBlock(), //nolint:staticcheck
	}
}

func dialUnix(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", addr)
}

func parseUnixEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme != "unix" {
		return "", fmt.Errorf("invalid scheme: %s (only supports 'unix' protocol)", u.Scheme)
	}
	return u.Path, nil
}

// connect creates a gRPC connection to the CRI service endpoint.
func connect(ctx context.Context, endpoint string) (*grpc.ClientConn, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("endpoint cannot be empty")
	}

	addr, err := parseUnixEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse endpoint: %w", err)
	}

	// Validate the socket file exists before attempting connection
	if _, err := os.Stat(addr); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("socket file does not exist: %s", addr)
		}
		return nil, fmt.Errorf("failed to stat socket file: %w", err)
	}

	// Attempt to establish connection with retries
	var conn *grpc.ClientConn
	var dialErr error
	for i := 0; i < 3; i++ {
		// "WithBlock" ctx cancel is no-op
		conn, dialErr = grpc.DialContext(ctx, addr, defaultDialOptions()...) //nolint:staticcheck
		if conn != nil && dialErr == nil {
			if conn.GetState() == connectivity.Ready {
				break
			}

			log.Logger.Warnw("connection is not ready, closing", "endpoint", endpoint, "connState", conn.GetState())
			_ = conn.Close()
			conn = nil
		} else {
			log.Logger.Warnw("failed to dial endpoint, retrying",
				"endpoint", endpoint,
				"attempt", i+1,
				"error", dialErr,
			)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}

	if dialErr != nil {
		return nil, fmt.Errorf("failed to establish connection after retries: %w", dialErr)
	}
	if conn == nil {
		return nil, fmt.Errorf("connection is nil")
	}

	log.Logger.Infow("successfully established connection", "endpoint", endpoint)
	return conn, nil
}

// createClient creates runtime and image service clients from a gRPC connection.
//
// Cannot use "k8s.io/kubernetes/pkg/kubelet/cri/remote.NewRemoteRuntimeService" directly
// as it causes a bunch of go module errors, importing the whole kubernetes repo.
// ref. https://github.com/kubernetes-sigs/cri-tools/blob/master/cmd/main.go
// ref. https://github.com/kubernetes/kubernetes/blob/v1.29.2/pkg/kubelet/cri/remote/remote_runtime.go
// ref. https://github.com/kubernetes/kubernetes/blob/v1.32.0-alpha.0/staging/src/k8s.io/cri-client/pkg/remote_runtime.go
func createClient(ctx context.Context, conn *grpc.ClientConn) (runtimeapi.RuntimeServiceClient, runtimeapi.ImageServiceClient, error) {
	// ref. https://github.com/kubernetes/kubernetes/blob/v1.32.0-alpha.0/staging/src/k8s.io/cri-client/pkg/remote_runtime.go
	runtimeClient := runtimeapi.NewRuntimeServiceClient(conn)
	version, err := runtimeClient.Version(ctx, &runtimeapi.VersionRequest{})
	if err != nil {
		return nil, nil, err
	}
	log.Logger.Debugw("successfully checked version", "version", version.String())

	status, err := runtimeClient.Status(ctx, &runtimeapi.StatusRequest{})
	if err != nil {
		return nil, nil, err
	}
	log.Logger.Debugw("successfully checked status", "status", status.String())

	imageClient := runtimeapi.NewImageServiceClient(conn)
	return runtimeClient, imageClient, nil
}

func checkContainerdInstalled() bool {
	p, err := pkg_file.LocateExecutable("containerd")
	if err == nil {
		log.Logger.Debugw("containerd found in PATH", "path", p)
		return true
	}
	log.Logger.Debugw("containerd not found in PATH", "error", err)
	return false
}

func checkSocketExists() bool {
	// if containerd is disabled or aborted (due to invalid config), the socket file will not exist
	// vice versa, if the socket file exists, containerd is running
	if _, err := os.Stat(defaultSocketFile); err != nil {
		if os.IsNotExist(err) {
			log.Logger.Debugw("containerd default socket file does not exist, skip containerd check", "file", defaultSocketFile)
		} else {
			log.Logger.Warnw("error checking containerd socket file, skip containerd check", "file", defaultSocketFile, "error", err)
		}
		return false
	}

	log.Logger.Debugw("containerd default socket file exists, containerd installed", "file", defaultSocketFile)
	return true
}

func checkContainerdRunning(ctx context.Context) bool {
	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()

	containerdRunning := false
	if conn, err := connect(cctx, defaultContainerRuntimeEndpoint); err == nil {
		log.Logger.Debugw("containerd default cri endpoint open, containerd running", "endpoint", defaultContainerRuntimeEndpoint)
		containerdRunning = true
		_ = conn.Close()
	} else {
		log.Logger.Debugw("containerd default cri endpoint not open, skip containerd checking", "endpoint", defaultContainerRuntimeEndpoint, "error", err)
	}

	if containerdRunning {
		log.Logger.Debugw("auto-detected containerd -- configuring containerd pod component")
		return true
	}
	return false
}

func listAllSandboxes(ctx context.Context, endpoint string) ([]PodSandbox, error) {
	conn, err := connect(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client, _, err := createClient(ctx, conn)
	if err != nil {
		return nil, err
	}

	listPodSandboxResp, err := client.ListPodSandbox(ctx, &runtimeapi.ListPodSandboxRequest{
		Filter: &runtimeapi.PodSandboxFilter{},
	})
	if err != nil {
		return nil, err
	}

	listContainersResp, err := client.ListContainers(ctx, &runtimeapi.ListContainersRequest{
		Filter: &runtimeapi.ContainerFilter{},
	})
	if err != nil {
		return nil, err
	}

	return convertToPodSandboxes(listPodSandboxResp, listContainersResp), nil
}

func convertToPodSandboxes(listPodSandboxResp *runtimeapi.ListPodSandboxResponse, listContainersResp *runtimeapi.ListContainersResponse) []PodSandbox {
	if listPodSandboxResp == nil || listContainersResp == nil {
		return nil
	}

	podSandboxes := make(map[string]PodSandbox, len(listPodSandboxResp.Items))
	for _, podSandbox := range listPodSandboxResp.Items {
		if podSandbox.Metadata == nil {
			continue
		}

		podSandboxes[podSandbox.Id] = PodSandbox{
			ID:        podSandbox.Id,
			Name:      podSandbox.Metadata.Name,
			Namespace: podSandbox.Metadata.Namespace,
			State:     podSandbox.State.String(),

			// to be filled in later
			Containers: nil,
		}

	}
	for _, container := range listContainersResp.Containers {
		podSandboxID := container.PodSandboxId
		podSandbox, ok := podSandboxes[podSandboxID]
		if !ok {
			log.Logger.Warnw("container found but pod sandbox not found", "container", container)
			continue
		}

		c := PodSandboxContainerStatus{
			ID:        container.Id,
			Name:      container.Metadata.Name,
			CreatedAt: container.CreatedAt,
			State:     container.State.String(),
		}
		if container.Image != nil {
			c.Image = container.Image.UserSpecifiedImage
		}
		podSandbox.Containers = append(podSandbox.Containers, c)

		podSandboxes[podSandboxID] = podSandbox
	}
	log.Logger.Debugw("listed pods", "pods", len(podSandboxes))

	pods := make([]PodSandbox, 0, len(podSandboxes))
	for _, s := range podSandboxes {
		pods = append(pods, s)
	}

	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace == pods[j].Namespace {
			return pods[i].Name < pods[j].Name
		}
		return pods[i].Namespace < pods[j].Namespace
	})
	return pods
}

// PodSandbox represents the pod information fetched from the local container runtime.
// Simplified version of k8s.io/cri-api/pkg/apis/runtime/v1.PodSandbox.
// ref. https://pkg.go.dev/k8s.io/cri-api/pkg/apis/runtime/v1#ListPodSandboxResponse
type PodSandbox struct {
	ID         string                      `json:"id,omitempty"`
	Namespace  string                      `json:"namespace,omitempty"`
	Name       string                      `json:"name,omitempty"`
	State      string                      `json:"state,omitempty"`
	Containers []PodSandboxContainerStatus `json:"containers,omitempty"`
}

// ref. https://pkg.go.dev/k8s.io/cri-api/pkg/apis/runtime/v1#ContainerStatus
type PodSandboxContainerStatus struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Image     string `json:"image,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	State     string `json:"state,omitempty"`
}
