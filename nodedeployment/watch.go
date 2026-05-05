package nodedeployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/urfave/cli/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	toolswatch "k8s.io/client-go/tools/watch"
)

var sndGVR = schema.GroupVersionResource{Group: "sei.io", Version: "v1alpha1", Resource: "seinodedeployments"}

// matchPhase decides whether a single SND event satisfies the --until
// condition. Returns (true, nil) on match, (false, error) on terminal
// Failed phase, (false, nil) otherwise so the watch keeps streaming.
func matchPhase(obj *unstructured.Unstructured, until string) (bool, error) {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if phase == until {
		return true, nil
	}
	if phase == "Failed" {
		msg, _, _ := unstructured.NestedString(obj.Object, "status", "plan", "failedTaskDetail", "error")
		if msg == "" {
			msg = "(no failedTaskDetail.error on status.plan)"
		}
		return false, fmt.Errorf("terminal Failed phase: %s", msg)
	}
	return false, nil
}

func watchAction(ctx context.Context, c *cli.Command) error {
	name := c.StringArg("name")
	if name == "" {
		emitStatus(os.Stderr, usageError("name argument required: seictl nd watch <name>"))
		return cli.Exit("", 1)
	}
	until := c.String("until")
	if until == "" {
		emitStatus(os.Stderr, usageError("--until=<phase> is required (e.g. --until=Ready)"))
		return cli.Exit("", 1)
	}
	timeout := c.Duration("timeout")

	kc := loadKubeconfig(c.String("kubeconfig"), c.String("namespace"))
	cfg, err := kc.RESTConfig()
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	ns, err := kc.Namespace()
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		emitStatus(os.Stderr, fmt.Errorf("build dynamic client: %w", err))
		return cli.Exit("", 1)
	}
	resource := dyn.Resource(sndGVR).Namespace(ns)
	fieldSelector := "metadata.name=" + name
	lw := &cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			opts.FieldSelector = fieldSelector
			return resource.List(ctx, opts)
		},
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			opts.FieldSelector = fieldSelector
			return resource.Watch(ctx, opts)
		},
	}

	enc := json.NewEncoder(os.Stdout)
	condition := func(event watch.Event) (bool, error) {
		if event.Type == watch.Error {
			return false, apierrors.FromObject(event.Object)
		}
		obj, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			return false, fmt.Errorf("unexpected object type %T", event.Object)
		}
		if err := enc.Encode(obj.Object); err != nil {
			return false, fmt.Errorf("encode NDJSON: %w", err)
		}
		return matchPhase(obj, until)
	}

	watchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	_, err = toolswatch.UntilWithSync(watchCtx, lw, &unstructured.Unstructured{}, nil, condition)
	if err != nil {
		// UntilWithSync returns wait.ErrWaitTimeout for both deadline
		// and cancellation; the watchCtx error preserves which one.
		if ctxErr := watchCtx.Err(); ctxErr != nil {
			err = ctxErr
		}
		emitStatus(os.Stderr, watchExitError(err, name, ns, until, timeout))
		return cli.Exit("", 1)
	}
	return nil
}

// watchExitError shapes the err that came out of UntilWithSync into a
// metav1.Status so stderr discrimination (`jq -r .reason`) covers
// timeout / NotFound / terminal-Failed-phase / transient API failure
// uniformly.
func watchExitError(err error, name, ns, until string, timeout time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &apierrors.StatusError{ErrStatus: metav1.Status{
			TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
			Status:   metav1.StatusFailure,
			Reason:   metav1.StatusReasonTimeout,
			Message:  fmt.Sprintf("watch %s/%s timed out after %s waiting for phase=%s", ns, name, timeout, until),
			Code:     http.StatusGatewayTimeout,
		}}
	}
	return err
}

var watchCmd = cli.Command{
	Name:      "watch",
	Usage:     "Stream SeiNodeDeployment events as NDJSON until a phase is reached",
	ArgsUsage: "<name>",
	Description: "Streams every SND event for <name> as one NDJSON line on " +
		"stdout, exiting 0 when .status.phase matches --until or 1 on " +
		"timeout, terminal Failed phase, or transient API error. " +
		"Discrimination on stderr via metav1.Status.reason (Timeout, " +
		"InternalError, etc.). Subsumes `kubectl wait --for=jsonpath=` " +
		"for orchestrator scripts.",
	Arguments: []cli.Argument{
		&cli.StringArg{Name: "name", UsageText: "metadata.name of the SeiNodeDeployment"},
	},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.StringFlag{
			Name:     "until",
			Required: true,
			Usage:    "Phase to wait for (e.g. --until=Ready). Matches .status.phase exactly.",
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Value: 15 * time.Minute,
			Usage: "Watch timeout; exits with metav1.Status reason=Timeout when exceeded",
		},
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig (honors KUBECONFIG colon-merge); defaults to $HOME/.kube/config or in-cluster",
		},
	},
	Action: watchAction,
}
