package kube

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// JobSnapshot exposes the subset of Job state that callers need to
// reason about Job immutability without importing batchv1.
type JobSnapshot struct {
	Found           bool
	ImageSHA        string // annotations["sei.io/image-sha"]
	DeadlineSeconds int64  // spec.activeDeadlineSeconds
}

// GetJobSnapshot returns {Found: false} on NotFound; other apiserver
// errors propagate.
func (c *Client) GetJobSnapshot(ctx context.Context, namespace, name string) (JobSnapshot, error) {
	cfg, err := c.flags.ToRESTConfig()
	if err != nil {
		return JobSnapshot{}, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return JobSnapshot{}, err
	}
	j, err := cs.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return JobSnapshot{Found: false}, nil
		}
		return JobSnapshot{}, err
	}
	snap := JobSnapshot{
		Found:    true,
		ImageSHA: j.Annotations["sei.io/image-sha"],
	}
	if j.Spec.ActiveDeadlineSeconds != nil {
		snap.DeadlineSeconds = *j.Spec.ActiveDeadlineSeconds
	}
	return snap, nil
}

// sndGVR identifies the SeiNodeDeployment CRD. We resolve via dynamic
// client + unstructured so seictl never imports the typed API surface
// from sei-k8s-controller (would create a dep cycle with sidecar/client).
var sndGVR = schema.GroupVersionResource{Group: "sei.io", Version: "v1alpha1", Resource: "seinodedeployments"}

// GetSND fetches a SeiNodeDeployment as Unstructured. Returns (nil, nil)
// on NotFound so callers can distinguish "no SND yet" from a hard
// apiserver error.
func (c *Client) GetSND(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	cfg, err := c.flags.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	u, err := dyn.Resource(sndGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}
