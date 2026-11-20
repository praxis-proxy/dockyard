package funcs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// defaultHelmTimeout is the wait timeout when a HelmChart sets Wait but no Timeout.
const defaultHelmTimeout = 5 * time.Minute

// HelmChart describes a chart release to upgrade-or-install through the Helm Go SDK
// — no helm binary. It is the structured replacement for shelling out to helm.
type HelmChart struct {
	// Release is the Helm release name.
	Release string
	// Chart is the chart name (resolved within RepoURL) or a local path / OCI ref.
	Chart string
	// RepoURL is the chart repository the chart name resolves within (https). Empty
	// if Chart is already a local path or OCI ref.
	RepoURL string
	// Version pins the chart version (empty => latest).
	Version string
	// Namespace is the release namespace.
	Namespace string
	// CreateNamespace creates Namespace if it does not exist (install only).
	CreateNamespace bool
	// Values are the chart values (equivalent to --set/-f merged).
	Values map[string]any
	// Wait blocks until the release's resources are ready.
	Wait bool
	// Timeout bounds the wait (0 => defaultHelmTimeout when Wait is set).
	Timeout time.Duration
}

// Helm upgrade-or-installs a chart release through the Helm SDK: if a release of
// that name already exists it is upgraded (idempotent on a reused cluster),
// otherwise it is installed. Pure in-process — no helm binary on PATH.
func Helm(c HelmChart) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, helmUpgradeInstall(ctx, cfg, c)
	}
}

func helmUpgradeInstall(ctx context.Context, cfg *envconf.Config, c HelmChart) error {
	if c.Wait && c.Timeout == 0 {
		c.Timeout = defaultHelmTimeout
	}
	actionCfg, settings, err := helmConfig(cfg, c.Namespace)
	if err != nil {
		return err
	}

	exists, err := releaseExists(actionCfg, c.Release)
	if err != nil {
		return err
	}

	if exists {
		up := action.NewUpgrade(actionCfg)
		up.Namespace = c.Namespace
		up.Wait = c.Wait
		up.Timeout = c.Timeout
		up.ChartPathOptions.RepoURL = c.RepoURL
		up.ChartPathOptions.Version = c.Version
		ch, err := loadChart(&up.ChartPathOptions, c.Chart, settings)
		if err != nil {
			return err
		}
		if _, err := up.RunWithContext(ctx, c.Release, ch, c.Values); err != nil {
			return fmt.Errorf("helm upgrade %s: %w", c.Release, err)
		}
		return nil
	}

	inst := action.NewInstall(actionCfg)
	inst.ReleaseName = c.Release
	inst.Namespace = c.Namespace
	inst.CreateNamespace = c.CreateNamespace
	inst.Wait = c.Wait
	inst.Timeout = c.Timeout
	inst.ChartPathOptions.RepoURL = c.RepoURL
	inst.ChartPathOptions.Version = c.Version
	ch, err := loadChart(&inst.ChartPathOptions, c.Chart, settings)
	if err != nil {
		return err
	}
	if _, err := inst.RunWithContext(ctx, ch, c.Values); err != nil {
		return fmt.Errorf("helm install %s: %w", c.Release, err)
	}
	return nil
}

// loadChart resolves a chart (downloading it from RepoURL if set) and loads it.
func loadChart(cpo *action.ChartPathOptions, name string, settings *cli.EnvSettings) (*chart.Chart, error) {
	path, err := cpo.LocateChart(name, settings)
	if err != nil {
		return nil, fmt.Errorf("locate chart %q: %w", name, err)
	}
	ch, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load chart %q: %w", path, err)
	}
	return ch, nil
}

func helmConfig(cfg *envconf.Config, namespace string) (*action.Configuration, *cli.EnvSettings, error) {
	settings := cli.New()

	kubeconfig := cfg.KubeconfigFile()
	ns := namespace
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &kubeconfig
	flags.Namespace = &ns

	actionCfg := new(action.Configuration)
	if err := actionCfg.Init(flags, namespace, "secret", func(format string, v ...any) {
		klog.V(4).Infof(format, v...)
	}); err != nil {
		return nil, nil, fmt.Errorf("init helm action config: %w", err)
	}

	regClient, err := registry.NewClient()
	if err != nil {
		return nil, nil, fmt.Errorf("new helm registry client: %w", err)
	}
	actionCfg.RegistryClient = regClient

	return actionCfg, settings, nil
}

func releaseExists(cfg *action.Configuration, name string) (bool, error) {
	hist := action.NewHistory(cfg)
	hist.Max = 1
	switch _, err := hist.Run(name); {
	case errors.Is(err, driver.ErrReleaseNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("helm history %s: %w", name, err)
	default:
		return true, nil
	}
}
