package components

import (
	"encoding/base64"
	"os"

	"github.com/ctfer-io/ctfer/internal"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv4 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v4"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Traefik struct {
	pulumi.ResourceState
}

var _ pulumi.ComponentResource = (*Traefik)(nil)

func NewTraefik(ctx *pulumi.Context, name string, args *TraefikArgs, opts ...pulumi.ResourceOption) (*Traefik, error) {
	tfk := &Traefik{}

	// remote chart url
	chartUrl := pulumi.String("oci://ghcr.io/traefik/helm/traefik").ToStringOutput()

	// offline chart url
	if internal.GetConfig().ChartsRepository != "" {
		chartUrl = pulumi.Sprintf("%s/traefik", internal.GetConfig().ChartsRepository)
	}

	// Regsiter the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:Traefik", name, tfk, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources (https://doc.traefik.io/traefik/getting-started/quick-start-with-kubernetes/)
	traefikLabels := pulumi.StringMap{
		"ctfer.io/app-name": pulumi.String("traefik"),
		"ctfer.io/part-of":  pulumi.String("ctfer"),
	}

	_, err = helmv4.NewChart(ctx, "traefik", &helmv4.ChartArgs{
		Namespace: args.Namespace,
		Version:   pulumi.String("35.2.0"),
		Chart:     chartUrl,
		SkipCrds:  pulumi.Bool(true), // we do not use crds for now
		Values: pulumi.Map{
			"image": pulumi.Map{
				"registry":   internal.GetConfig().ImagesRepository,
				"repository": pulumi.String("library/traefik"),
			},
			"deployment": pulumi.Map{
				"replicas":  pulumi.Int(3), // TODO mke it configurable
				"podLabels": traefikLabels,
			},
			"providers": pulumi.Map{
				"kubernetesCRD": pulumi.Map{
					"enabled": pulumi.Bool(false),
				},
				"kubernetesIngress": pulumi.Map{
					"allowCrossNamespace": pulumi.Bool(true), // challenge on-demand
					// "allowExternalNameServices": pulumi.Bool(true), // if keda enabled
				},
			},
			"ports": pulumi.Map{
				"web": pulumi.Map{
					"redirections": pulumi.Map{
						"entryPoint": pulumi.Map{
							"scheme": pulumi.String("https"),
							"to":     pulumi.String("websecure"),
						},
					},
				},
			},
			"globalArguments": pulumi.StringArray{}, // disable check version
			"additionalArguments": pulumi.StringArray{
				pulumi.String("--api.insecure=true"), // enable dashboard on port 8080
			},
		},
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}

	// Create TLS secret
	// TODO make it configurable
	tlsCrt, err := os.ReadFile("certs/ctfd.crt")
	if err != nil {
		return nil, err
	}
	tlsKey, err := os.ReadFile("certs/ctfd.key")
	if err != nil {
		return nil, err
	}
	_, err = corev1.NewSecret(ctx, "domain-tls-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("domain-tls-secret"),
			Namespace: args.Namespace,
		},
		Data: pulumi.ToStringMap(map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(tlsCrt),
			"tls.key": base64.StdEncoding.EncodeToString(tlsKey),
		}),
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}
	return tfk, nil
}

type TraefikArgs struct {
	Namespace pulumi.String
}
