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
		ns, err := corev1.NewNamespace(ctx, "ctf-ns", &corev1.NamespaceArgs{
			Metadata: metav1.ObjectMetaArgs{
				Labels: pulumi.StringMap{
					"app.kubernetes.io/component": pulumi.String("ctfer"),
					"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				},
				Name: pulumi.String(cfg.Namespace),
			},
		})
		if err != nil {
			return err
		}

		// Grant DNS resolution
		_, err = netwv1.NewNetworkPolicy(ctx, "grant-dns", &netwv1.NetworkPolicyArgs{
			Metadata: metav1.ObjectMetaArgs{
				Namespace: ns.Metadata.Name().Elem(),
				Labels: pulumi.StringMap{
					"app.kubernetes.io/component": pulumi.String("ctfer"),
					"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
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
			Namespace: pulumi.String(cfg.Namespace),
			Platform: &services.PlatformArgs{
				Image:              pulumi.String(cfg.Platform.Image),
				ChallManagerURL:    pulumi.String(cfg.ChallManagerURL),
				Hostname:           pulumi.String(cfg.Platform.Hostname),
				Crt:                pulumi.String(cfg.Platform.Crt),
				Key:                pulumi.String(cfg.Platform.Key),
				Workers:            pulumi.Int(cfg.Platform.Workers),
				Replicas:           pulumi.Int(cfg.Platform.Replicas),
				Requests:           pulumi.ToStringMap(cfg.Platform.Requests),
				Limits:             pulumi.ToStringMap(cfg.Platform.Limits),
				StorageSize:        pulumi.String(cfg.Platform.StorageSize),
				StorageClassName:   pulumi.String(cfg.Platform.StorageClassName),
				PVCAccessModes:     pulumi.ToStringArray(cfg.Platform.PVCAccessModes),
				IngressAnnotations: pulumi.ToStringMap(cfg.Platform.IngressAnnotations),
			},
			DB: &services.DBArgs{
				StorageClassName:  pulumi.String(cfg.DB.StorageClassName),
				OperatorNamespace: pulumi.String(cfg.DB.OperatorNamespace),
				Replicas:          pulumi.Int(cfg.DB.Replicas),
			},
			Cache: &services.CacheArgs{
				Replicas: pulumi.Int(cfg.Cache.Replicas),
			},
			ChartsRepository: pulumi.String(cfg.ChartsRepository),
			ImagesRepository: pulumi.String(cfg.ImagesRepository),
			IngressNamespace: pulumi.String(cfg.IngressNamespace),
			IngressLabels:    pulumi.ToStringMap(cfg.IngressLabels),
		}
		if cfg.OTel != nil {
			ctferArgs.OTel = &common.OTelArgs{
				ServiceName: pulumi.String(ctx.Stack()),
				Endpoint:    pulumi.String(cfg.OTel.Endpoint),
				Insecure:    cfg.OTel.Insecure,
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
		ImagesRepository string
		ChartsRepository string
		ChallManagerURL  string
		IngressNamespace string
		IngressLabels    map[string]string

		Platform *PlatformConfig
		Cache    *CacheConfig
		DB       *DBConfig
		OTel     *OTelConfig
	}

	PlatformConfig struct {
		Image              string            `json:"image"`
		Hostname           string            `json:"hostname"`
		Crt                string            `json:"crt"`
		Key                string            `json:"key"`
		Workers            int               `json:"workers"`
		Replicas           int               `json:"replicas"`
		Requests           map[string]string `json:"requests"`
		Limits             map[string]string `json:"limits"`
		StorageSize        string            `json:"storage-size"`
		StorageClassName   string            `json:"storage-class-name"`
		PVCAccessModes     []string          `json:"pvc-access-modes"`
		IngressAnnotations map[string]string `json:"ingress-annotations"`
	}

	CacheConfig struct {
		Replicas int `json:"replicas"`
	}

	DBConfig struct {
		StorageClassName  string `json:"storage-class-name"`
		OperatorNamespace string `json:"operator-namespace"`
		Replicas          int    `json:"replicas"`
	}

	OTelConfig struct {
		Endpoint string `json:"endpoint"`
		Insecure bool   `json:"insecure"`
	}
)

func loadConfig(ctx *pulumi.Context) (*Config, error) {
	cfg := config.New(ctx, "")
	c := &Config{
		Namespace:        cfg.Get("namespace"),
		ImagesRepository: cfg.Get("images-repository"),
		ChartsRepository: cfg.Get("charts-repository"),
		ChallManagerURL:  cfg.Get("chall-manager-url"),
		IngressNamespace: cfg.Get("ingress-namespace"),
		Platform:         &PlatformConfig{},
		Cache:            &CacheConfig{},
		DB:               &DBConfig{},
	}

	// As we cannot default this one, we silently drop the error is not are set
	_ = cfg.TryObject("ingress-labels", &c.IngressLabels)

	if err := cfg.TryObject("platform", c.Platform); err != nil {
		return nil, err
	}

	if err := cfg.TryObject("cache", c.Cache); err != nil {
		return nil, err
	}

	if err := cfg.TryObject("db", c.DB); err != nil {
		return nil, err
	}

	var otelC OTelConfig
	if err := cfg.TryObject("otel", &otelC); err == nil && otelC.Endpoint != "" {
		c.OTel = &otelC
	}

	return c, nil
}
