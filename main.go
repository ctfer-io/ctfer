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
			AccessMode:       pulumi.String(cfg.AccessMode),
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
		}
		if cfg.Otel != nil {
			ctferArgs.Otel = &common.OtelArgs{
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
		AccessMode       string
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
		AccessMode:       cfg.Get("access-mode"),
		CTFdImage:        cfg.Get("ctfd-image"),
		ChallManagerUrl:  cfg.Get("chall-manager-url"),
		CTFdCrt:          cfg.GetSecret("ctfd-crt"),
		CTFdKey:          cfg.GetSecret("ctfd-key"),
		CTFdStorageSize:  cfg.Get("ctfd-storage-size"),
		CTFdReplicas:     cfg.GetInt("ctfd-replicas"),
		CTFdWorkers:      cfg.GetInt("ctfd-workers"),
		IngressNamespace: cfg.Get("ingress-namespace"),
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

	var otelC OtelConfig
	if err := cfg.TryObject("otel", &otelC); err == nil && otelC.Endpoint != "" {
		c.Otel = &otelC
	}

	return c, nil
}
