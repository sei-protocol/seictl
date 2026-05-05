package nodedeployment

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sei-protocol/seictl/internal/snd"
)

func parseCascade(raw string) (*metav1.DeletionPropagation, error) {
	switch raw {
	case "", "foreground":
		p := metav1.DeletePropagationForeground
		return &p, nil
	case "background":
		p := metav1.DeletePropagationBackground
		return &p, nil
	case "orphan":
		p := metav1.DeletePropagationOrphan
		return &p, nil
	}
	return nil, fmt.Errorf("invalid --cascade %q (use foreground|background|orphan)", raw)
}

func deleteAction(ctx context.Context, c *cli.Command) error {
	name := c.StringArg("name")
	if name == "" {
		emitStatus(os.Stderr, usageError("name argument required: seictl nd delete <name>"))
		return cli.Exit("", 1)
	}

	cascade, err := parseCascade(c.String("cascade"))
	if err != nil {
		emitStatus(os.Stderr, usageError("%s", err.Error()))
		return cli.Exit("", 1)
	}

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

	kcli, err := newClient(cfg)
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	obj := snd.New()
	obj.SetName(name)
	obj.SetNamespace(ns)
	if err := kcli.Delete(ctx, obj, &client.DeleteOptions{PropagationPolicy: cascade}); err != nil {
		emitStatus(os.Stderr, fmt.Errorf("delete SeiNodeDeployment %s/%s: %w", ns, name, err))
		return cli.Exit("", 1)
	}
	fmt.Printf("seinodedeployment.sei.io/%s deleted\n", name)
	return nil
}

var deleteCmd = cli.Command{
	Name:      "delete",
	Usage:     "Delete a SeiNodeDeployment by name",
	ArgsUsage: "<name>",
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
			Name:  "cascade",
			Value: "foreground",
			Usage: "Propagation policy: foreground (default) | background | orphan",
		},
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig (honors KUBECONFIG colon-merge); defaults to $HOME/.kube/config or in-cluster",
		},
	},
	Action: deleteAction,
}
