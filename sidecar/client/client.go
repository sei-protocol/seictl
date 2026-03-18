package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const DefaultPort int32 = 7777

// ErrNotFound is returned when the requested task does not exist (HTTP 404).
var ErrNotFound = errors.New("sidecar: task not found")

// SidecarClient wraps the generated ClientWithResponses with a simpler,
// error-oriented API.
type SidecarClient struct {
	inner *ClientWithResponses
}

// Option configures optional SidecarClient parameters.
type Option func(*sidecarOpts)

type sidecarOpts struct {
	httpClient HttpRequestDoer
	timeout    time.Duration
}

// WithHTTPDoer overrides the underlying HTTP transport.
func WithHTTPDoer(doer HttpRequestDoer) Option {
	return func(o *sidecarOpts) { o.httpClient = doer }
}

// WithTimeout sets the HTTP client timeout. Defaults to 10s.
func WithTimeout(d time.Duration) Option {
	return func(o *sidecarOpts) { o.timeout = d }
}

// NewSidecarClient creates a client from an explicit base URL.
func NewSidecarClient(baseURL string, opts ...Option) (*SidecarClient, error) {
	o := sidecarOpts{timeout: 10 * time.Second}
	for _, fn := range opts {
		fn(&o)
	}

	var clientOpts []ClientOption
	if o.httpClient != nil {
		clientOpts = append(clientOpts, WithHTTPClient(o.httpClient))
	} else {
		clientOpts = append(clientOpts, WithHTTPClient(&http.Client{Timeout: o.timeout}))
	}

	inner, err := NewClientWithResponses(baseURL, clientOpts...)
	if err != nil {
		return nil, err
	}
	return &SidecarClient{inner: inner}, nil
}

// NewSidecarClientFromPodDNS builds a client targeting the sidecar via
// Kubernetes headless-service DNS:
//
//	http://{name}-0.{name}.{namespace}.svc.cluster.local:{port}
func NewSidecarClientFromPodDNS(name, namespace string, port int32, opts ...Option) (*SidecarClient, error) {
	if port == 0 {
		port = DefaultPort
	}
	baseURL := fmt.Sprintf("http://%s-0.%s.%s.svc.cluster.local:%d", name, name, namespace, port)
	return NewSidecarClient(baseURL, opts...)
}

// Status queries the sidecar's current lifecycle state.
func (c *SidecarClient) Status(ctx context.Context) (*StatusResponse, error) {
	resp, err := c.inner.GetStatusWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying sidecar status: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("sidecar status returned %d: %s", resp.StatusCode(), bytes.TrimSpace(resp.Body))
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("sidecar status returned 200 but empty body")
	}
	return resp.JSON200, nil
}

// SubmitTask sends a TaskRequest to the sidecar. This is the generic
// submission path used internally by the typed Submit*Task methods and
// by the controller for dynamic dispatch. Prefer the typed methods for
// compile-time validation of task parameters.
func (c *SidecarClient) SubmitTask(ctx context.Context, task TaskRequest) (uuid.UUID, error) {
	resp, err := c.inner.SubmitTaskWithResponse(ctx, task)
	if err != nil {
		return uuid.Nil, fmt.Errorf("submitting task to sidecar: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusCreated:
		if resp.JSON201 == nil {
			return uuid.Nil, fmt.Errorf("sidecar returned 201 but no task ID in response body")
		}
		return resp.JSON201.Id, nil
	case http.StatusBadRequest:
		if resp.JSON400 != nil {
			return uuid.Nil, fmt.Errorf("sidecar rejected task: %s", resp.JSON400.Error)
		}
		return uuid.Nil, fmt.Errorf("sidecar rejected task: %s", bytes.TrimSpace(resp.Body))
	default:
		return uuid.Nil, fmt.Errorf("sidecar task submission returned %d: %s", resp.StatusCode(), bytes.TrimSpace(resp.Body))
	}
}

// ListTasks returns recent task results and active scheduled tasks.
func (c *SidecarClient) ListTasks(ctx context.Context) ([]TaskResult, error) {
	resp, err := c.inner.ListTasksWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing sidecar tasks: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("sidecar list tasks returned %d: %s", resp.StatusCode(), bytes.TrimSpace(resp.Body))
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return *resp.JSON200, nil
}

// GetTask retrieves a single task result by ID.
func (c *SidecarClient) GetTask(ctx context.Context, id uuid.UUID) (*TaskResult, error) {
	resp, err := c.inner.GetTaskWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("getting sidecar task %s: %w", id, err)
	}
	switch resp.StatusCode() {
	case http.StatusOK:
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("sidecar returned 200 for task %s but empty body", id)
		}
		return resp.JSON200, nil
	case http.StatusNotFound:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("sidecar get task returned %d: %s", resp.StatusCode(), bytes.TrimSpace(resp.Body))
	}
}

// DeleteTask removes a task result or cancels a scheduled task.
func (c *SidecarClient) DeleteTask(ctx context.Context, id uuid.UUID) error {
	resp, err := c.inner.DeleteTaskWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting sidecar task %s: %w", id, err)
	}
	switch resp.StatusCode() {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("sidecar delete task returned %d: %s", resp.StatusCode(), bytes.TrimSpace(resp.Body))
	}
}

// Healthz checks whether the sidecar is healthy.
// Returns (true, nil) for 200, (false, nil) for 503, and (false, error)
// for network failures or unexpected status codes.
func (c *SidecarClient) Healthz(ctx context.Context) (bool, error) {
	resp, err := c.inner.HealthzWithResponse(ctx)
	if err != nil {
		return false, fmt.Errorf("querying sidecar healthz: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK:
		return true, nil
	case http.StatusServiceUnavailable:
		return false, nil
	default:
		return false, fmt.Errorf("sidecar healthz returned %d: %s", resp.StatusCode(), bytes.TrimSpace(resp.Body))
	}
}

// ---------------------------------------------------------------------------
// Typed submit methods -- primary public API for task submission.
// Each validates the typed struct and delegates to SubmitTask.
// ---------------------------------------------------------------------------

func (c *SidecarClient) SubmitSnapshotRestoreTask(ctx context.Context, task SnapshotRestoreTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitSnapshotUploadTask(ctx context.Context, task SnapshotUploadTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitConfigureGenesisTask(ctx context.Context, task ConfigureGenesisTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitDiscoverPeersTask(ctx context.Context, task DiscoverPeersTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitConfigPatchTask(ctx context.Context, task ConfigPatchTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitConfigApplyTask(ctx context.Context, task ConfigApplyTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitConfigValidateTask(ctx context.Context, task ConfigValidateTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitConfigReloadTask(ctx context.Context, task ConfigReloadTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitMarkReadyTask(ctx context.Context, task MarkReadyTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitConfigureStateSyncTask(ctx context.Context, task ConfigureStateSyncTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}

func (c *SidecarClient) SubmitResultExportTask(ctx context.Context, task ResultExportTask) (uuid.UUID, error) {
	if err := task.Validate(); err != nil {
		return uuid.Nil, fmt.Errorf("task validation failed: %w", err)
	}
	return c.SubmitTask(ctx, task.ToTaskRequest())
}
