package main

import (
	"github.com/ctfer-io/ctfer/services"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg, err := loadConfig(ctx)
		if err != nil {
			return err
		}

		// Create CTF's namespace
		if _, err := corev1.NewNamespace(ctx, "namespace", &corev1.NamespaceArgs{
			Metadata: metav1.ObjectMetaArgs{
				Labels: pulumi.StringMap{
					"ctfer.io/app-name": pulumi.String("ctfd"),
					"ctfer.io/part-of":  pulumi.String("ctfer"),
				},
				Name: pulumi.String(cfg.Namespace),
			},
		}); err != nil {
			return err
		}

		ctfer, err := services.NewCTFer(ctx, ctx.Stack(), &services.CTFerArgs{
			Namespace:        pulumi.String(cfg.Namespace),
			CTFdImage:        pulumi.String(cfg.CTFdImage),
			Hostname:         pulumi.String(cfg.Hostname),
			CTFdCrt:          cfg.CTFdCrt,
			CTFdKey:          cfg.CTFdKey,
			CTFdStorageSize:  pulumi.String(cfg.CTFdStorageSize),
			CTFdWorkers:      pulumi.Int(cfg.CTFdWorkers),
			CTFdReplicas:     pulumi.Int(cfg.CTFdReplicas),
			ChartsRepository: pulumi.String(cfg.ChartsRepository),
			ImagesRepository: pulumi.String(cfg.ImagesRepository),
			ChallManagerUrl:  pulumi.String(cfg.ChallManagerUrl),
			CTFdRequests:     pulumi.ToStringMap(cfg.CTFdRequests),
			CTFdLimits:       pulumi.ToStringMap(cfg.CTFdLimits),
			IngressNamespace: pulumi.String(cfg.IngressNamespace),
			IngressLabels:    pulumi.ToStringMap(cfg.IngressLabels),
		})
		if err != nil {
			return err
		}

		ctx.Export("url", ctfer.URL)
		return nil
	})
}

// Config holds the values configured using pulumi CLI.
type Config struct {
	// Namespace in which ctfer will deploy the CTF.
	Namespace        string
	Hostname         string
	ImagesRepository string
	ChartsRepository string
	CTFdImage        string
	ChallManagerUrl  string
	CTFdStorageSize  string
	CTFdCrt          pulumi.StringInput
	CTFdKey          pulumi.StringInput
	CTFdReplicas     int
	CTFdWorkers      int
	CTFdRequests     map[string]string
	CTFdLimits       map[string]string
	IngressNamespace string
	IngressLabels    map[string]string
}

func loadConfig(ctx *pulumi.Context) (*Config, error) {
	cfg := config.New(ctx, "")
	c := &Config{
		Namespace:        cfg.Get("namespace"),
		Hostname:         cfg.Get("hostname"),
		ImagesRepository: cfg.Get("images-repository"),
		ChartsRepository: cfg.Get("charts-repository"),
		CTFdImage:        cfg.Get("ctfd-image"),
		ChallManagerUrl:  cfg.Get("chall-manager-url"),
		CTFdCrt:          cfg.GetSecret("ctfd-crt"),
		CTFdKey:          cfg.GetSecret("ctfd-key"),
		CTFdStorageSize:  cfg.Get("ctfd-storage-size"),
		CTFdReplicas:     cfg.GetInt("ctfd-replicas"),
		CTFdWorkers:      cfg.GetInt("ctfd-workers"),
	}
	if err := cfg.TryObject("ctfd-requests", &c.CTFdRequests); err != nil {
		return nil, err
	}

	if err := cfg.TryObject("ctfd-limits", &c.CTFdLimits); err != nil {
		return nil, err
	}

	if err := cfg.TryObject("ingress-labels", &c.IngressLabels); err != nil {
		return nil, err
	}

	return c, nil
}
