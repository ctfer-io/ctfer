package main

import (
	"github.com/ctfer-io/ctfer/services"
	"github.com/ctfer-io/ctfer/services/common"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	netwv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
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
		ns, err := corev1.NewNamespace(ctx, "namespace", &corev1.NamespaceArgs{
			Metadata: metav1.ObjectMetaArgs{
				Labels: pulumi.StringMap{
					"ctfer.io/app-name": pulumi.String("ctfd"),
					"ctfer.io/part-of":  pulumi.String("ctfer"),
				},
				Name: pulumi.String(cfg.Namespace),
			},
		})
		if err != nil {
			return err
		}

		// Grant DNS resolution
		_, err = netwv1.NewNetworkPolicy(ctx, "dns", &netwv1.NetworkPolicyArgs{
			Metadata: metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name().Elem(),
				Labels: pulumi.StringMap{
					"ctfer.io/app-name": pulumi.String("ctfd"),
					"ctfer.io/part-of":  pulumi.String("ctfer"),
				},
			},
			Spec: netwv1.NetworkPolicySpecArgs{
				PolicyTypes: pulumi.ToStringArray([]string{
					"Egress",
				}),
				PodSelector: metav1.LabelSelectorArgs{},
				Egress: netwv1.NetworkPolicyEgressRuleArray{
					netwv1.NetworkPolicyEgressRuleArgs{
						To: netwv1.NetworkPolicyPeerArray{
							netwv1.NetworkPolicyPeerArgs{
								NamespaceSelector: metav1.LabelSelectorArgs{
									MatchLabels: pulumi.StringMap{
										"kubernetes.io/metadata.name": pulumi.String("kube-system"),
									},
								},
								PodSelector: metav1.LabelSelectorArgs{
									MatchLabels: pulumi.StringMap{
										"k8s-app": pulumi.String("kube-dns"),
									},
								},
							},
						},
						Ports: netwv1.NetworkPolicyPortArray{
							netwv1.NetworkPolicyPortArgs{
								Port:     pulumi.Int(53),
								Protocol: pulumi.String("UDP"),
							},
							netwv1.NetworkPolicyPortArgs{
								Port:     pulumi.Int(53),
								Protocol: pulumi.String("TCP"),
							},
						},
					},
				},
			},
		})
		if err != nil {
			return err
		}

		ctferArgs := &services.CTFerArgs{
			Namespace:        pulumi.String(cfg.Namespace),
			CTFdImage:        pulumi.String(cfg.CTFdImage),
			Hostname:         pulumi.String(cfg.Hostname),
			StorageClassName: pulumi.String(cfg.StorageClassName),
			PVCAccessModes: pulumi.ToStringArray([]string{
				cfg.PVCAccessMode,
			}),
			Crt:              cfg.Crt,
			Key:              cfg.Key,
			StorageSize:      pulumi.String(cfg.StorageSize),
			Workers:          pulumi.Int(cfg.Workers),
			Replicas:         pulumi.Int(cfg.Replicas),
			ChartsRepository: pulumi.String(cfg.ChartsRepository),
			ImagesRepository: pulumi.String(cfg.ImagesRepository),
			ChallManagerURL:  pulumi.String(cfg.ChallManagerUrl),
			Requests:         pulumi.ToStringMap(cfg.Requests),
			Limits:           pulumi.ToStringMap(cfg.Limits),
			IngressNamespace: pulumi.String(cfg.IngressNamespace),
			IngressLabels:    pulumi.ToStringMap(cfg.IngressLabels),
		}
		if cfg.Otel != nil {
			ctferArgs.OTel = &common.OTelArgs{
				ServiceName: pulumi.String(ctx.Stack()),
				Endpoint:    pulumi.String(cfg.Otel.Endpoint),
				Insecure:    cfg.Otel.Insecure,
			}
		}
		ctfer, err := services.NewCTFer(ctx, ctx.Stack(), ctferArgs)
		if err != nil {
			return err
		}

		ctx.Export("url", ctfer.URL)
		return nil
	})
}

type (
	// Config holds the values configured using pulumi CLI.
	Config struct {
		// Namespace in which ctfer will deploy the CTF.
		Namespace        string
		Hostname         string
		ImagesRepository string
		ChartsRepository string
		StorageClassName string
		PVCAccessMode    string
		CTFdImage        string
		ChallManagerUrl  string
		StorageSize      string
		Crt              pulumi.StringInput
		Key              pulumi.StringInput
		Replicas         int
		Workers          int
		Requests         map[string]string
		Limits           map[string]string
		IngressNamespace string
		IngressLabels    map[string]string

		Otel *OtelConfig
	}

	OtelConfig struct {
		Endpoint string `json:"endpoint"`
		Insecure bool   `json:"insecure"`
	}
)

func loadConfig(ctx *pulumi.Context) (*Config, error) {
	cfg := config.New(ctx, "")
	c := &Config{
		Namespace:        cfg.Get("namespace"),
		Hostname:         cfg.Get("hostname"),
		ImagesRepository: cfg.Get("images-repository"),
		ChartsRepository: cfg.Get("charts-repository"),
		StorageClassName: cfg.Get("storage-class-name"),
		PVCAccessMode:    cfg.Get("pvc-access-mode"),
		CTFdImage:        cfg.Get("ctfd-image"),
		ChallManagerUrl:  cfg.Get("chall-manager-url"),
		Crt:              cfg.GetSecret("crt"),
		Key:              cfg.GetSecret("key"),
		StorageSize:      cfg.Get("storage-size"),
		Replicas:         cfg.GetInt("replicas"),
		Workers:          cfg.GetInt("workers"),
		IngressNamespace: cfg.Get("ingress-namespace"),
	}
	if err := cfg.TryObject("requests", &c.Requests); err != nil {
		return nil, err
	}

	if err := cfg.TryObject("limits", &c.Limits); err != nil {
		return nil, err
	}

	if err := cfg.TryObject("ingress-labels", &c.IngressLabels); err != nil {
		return nil, err
	}

	var otelC OtelConfig
	if err := cfg.TryObject("otel", &otelC); err == nil && otelC.Endpoint != "" {
		c.Otel = &otelC
	}

	return c, nil
}
