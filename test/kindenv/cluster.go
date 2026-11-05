package kindenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	kindcluster "sigs.k8s.io/kind/pkg/cluster"
)

// Cluster is a bare kind cluster for checks that install their own stack.
type Cluster struct {
	e *env
}

// NewCluster returns a handle on the kind cluster name with its kubeconfig at kubeconfig. It
// creates nothing; progress lines go to out, or stdout when out is nil.
func NewCluster(name, kubeconfig string, out io.Writer) (*Cluster, error) {
	e, err := newEnv(Config{Name: name, Kubeconfig: kubeconfig, Out: out})
	if err != nil {
		return nil, err
	}
	return &Cluster{e: e}, nil
}

// Create deletes any cluster of the same name, then creates it from config and nodeImage. A nil
// config or an empty nodeImage keeps kind's default.
func (c *Cluster) Create(nodeImage string, config *v1alpha4.Cluster) error {
	ok, err := c.e.exists()
	if err != nil {
		return err
	}
	if ok {
		c.e.logf("deleting leftover cluster %s", c.e.cfg.Name)
		if err := c.e.provider.Delete(c.e.cfg.Name, c.e.cfg.Kubeconfig); err != nil {
			return fmt.Errorf("delete kind cluster %s: %w", c.e.cfg.Name, err)
		}
	}
	c.e.logf("creating kind cluster %s", c.e.cfg.Name)
	opts := []kindcluster.CreateOption{
		kindcluster.CreateWithKubeconfigPath(c.e.cfg.Kubeconfig),
		kindcluster.CreateWithWaitForReady(120 * time.Second),
		kindcluster.CreateWithDisplayUsage(false),
		kindcluster.CreateWithDisplaySalutation(false),
	}
	if config != nil {
		opts = append(opts, kindcluster.CreateWithV1Alpha4Config(config))
	}
	if nodeImage != "" {
		opts = append(opts, kindcluster.CreateWithNodeImage(nodeImage))
	}
	if err := c.e.provider.Create(c.e.cfg.Name, opts...); err != nil {
		return fmt.Errorf("create kind cluster %s: %w", c.e.cfg.Name, err)
	}
	return nil
}

// Delete deletes the cluster and its kubeconfig. A missing cluster is not an error.
func (c *Cluster) Delete() error {
	if err := c.e.provider.Delete(c.e.cfg.Name, c.e.cfg.Kubeconfig); err != nil {
		return fmt.Errorf("delete kind cluster %s: %w", c.e.cfg.Name, err)
	}
	if err := os.Remove(c.e.cfg.Kubeconfig); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Pull pulls each image on every node that lacks it, retrying as Up does.
func (c *Cluster) Pull(ctx context.Context, images ...string) error {
	for _, image := range images {
		if err := c.e.pullOnNodes(ctx, image); err != nil {
			return err
		}
	}
	return nil
}

// PullDeployments pulls the container images of every Deployment in a multi-document manifest.
func (c *Cluster) PullDeployments(ctx context.Context, manifest []byte) error {
	images, err := deploymentImages(manifest)
	if err != nil {
		return err
	}
	return c.Pull(ctx, images...)
}
