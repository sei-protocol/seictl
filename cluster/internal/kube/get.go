package kube

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
