package components

import (
	"encoding/base64"
	"fmt"
	"os"
	"sync"

	"github.com/hashicorp/go-multierror"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv4 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v4"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	defaultTraefikChartURL = "oci://ghcr.io/traefik/helm/traefik"
)

type Traefik struct {
	pulumi.ResourceState

	chart *helmv4.Chart
	sec   *corev1.Secret
}

type TraefikArgs struct {
	Namespace        pulumi.StringInput
	ChartsRepository pulumi.StringInput
	ChartVersion     pulumi.StringInput
	Registry         pulumi.StringInput

	chartUrl pulumi.StringOutput
}

func NewTraefik(ctx *pulumi.Context, name string, args *TraefikArgs, opts ...pulumi.ResourceOption) (*Traefik, error) {
	tfk := &Traefik{}
	args = tfk.defaults(args)
	if err := tfk.check(args); err != nil {
		return nil, err
	}
	err := ctx.RegisterComponentResource("ctfer-io:ctfer:traefik", name, tfk, opts...)
	if err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(tfk))

	if err := tfk.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := tfk.outputs(ctx); err != nil {
		return nil, err
	}

	return tfk, nil
}

func (tfk *Traefik) defaults(args *TraefikArgs) *TraefikArgs {
	if args == nil {
		args = &TraefikArgs{}
	}

	args.chartUrl = pulumi.String(defaultTraefikChartURL).ToStringOutput()
	if args.ChartsRepository != nil {
		args.chartUrl = args.ChartsRepository.ToStringOutput().ApplyT(func(chartRepository string) string {
			if chartRepository == "" {
				return defaultTraefikChartURL
			}
			return fmt.Sprintf("%s/traefik", chartRepository)
		}).(pulumi.StringOutput)
	}

	return args
}

func (tfk *Traefik) check(_ *TraefikArgs) error {
	checks := 0
	wg := &sync.WaitGroup{}
	wg.Add(checks)
	cerr := make(chan error, checks)

	// TODO perform validation checks
	// smth.ApplyT(func(abc def) ghi {
	//     defer wg.Done()
	//
	//     ... the actual test
	//     if err != nil {
	//         cerr <- err
	//         return
	//     }
	// })

	wg.Wait()
	close(cerr)

	var merr error
	for err := range cerr {
		merr = multierror.Append(merr, err)
	}
	return merr
}

func (tfk *Traefik) provision(ctx *pulumi.Context, args *TraefikArgs, opts ...pulumi.ResourceOption) (err error) {
	traefikLabels := pulumi.StringMap{
		"ctfer.io/app-name": pulumi.String("traefik"),
		"ctfer.io/part-of":  pulumi.String("ctfer"),
	}

	tfk.chart, err = helmv4.NewChart(ctx, "traefik", &helmv4.ChartArgs{
		Namespace: args.Namespace,
		Version:   pulumi.String("35.2.0"),
		Chart:     args.chartUrl,
		SkipCrds:  pulumi.Bool(true), // we do not use crds for now
		Values: pulumi.Map{
			"image": pulumi.Map{
				"registry": args.Registry.ToStringOutput().ApplyT(func(repo string) *string {
					if repo != "" {
						return &repo
					}
					return nil
				}),
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
				"websecure": pulumi.Map{
					"asDefault": pulumi.Bool(true),
				},
			},
			"globalArguments": pulumi.StringArray{}, // disable check version
			"additionalArguments": pulumi.StringArray{
				pulumi.String("--api.insecure=true"), // enable dashboard on port 8080
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// Create TLS secret
	// TODO make it configurable
	tlsCrt, err := os.ReadFile("certs/ctfd.crt")
	if err != nil {
		return err
	}
	tlsKey, err := os.ReadFile("certs/ctfd.key")
	if err != nil {
		return err
	}
	tfk.sec, err = corev1.NewSecret(ctx, "domain-tls-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("domain-tls-secret"),
			Namespace: args.Namespace,
		},
		Data: pulumi.ToStringMap(map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(tlsCrt),
			"tls.key": base64.StdEncoding.EncodeToString(tlsKey),
		}),
	}, opts...)
	if err != nil {
		return
	}

	return
}

func (tfk *Traefik) outputs(ctx *pulumi.Context) error {
	return ctx.RegisterResourceOutputs(tfk, pulumi.Map{})
}
