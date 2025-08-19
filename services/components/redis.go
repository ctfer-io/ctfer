package components

import (
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/go-multierror"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv4 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v4"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	defaultRedisChartURL = "oci://registry-1.docker.io/bitnamicharts/redis"
)

type Redis struct {
	pulumi.ResourceState

	pass  *random.RandomPassword
	sec   *corev1.Secret
	chart *helmv4.Chart

	URL       pulumi.StringOutput
	PodLabels pulumi.StringMapOutput
}

type RedisArgs struct {
	Namespace        pulumi.StringInput
	ChartsRepository pulumi.StringInput
	ChartVersion     pulumi.StringInput
	Registry         pulumi.StringInput

	StorageClassName pulumi.StringInput
	storageClassName pulumi.StringPtrOutput

	AccessMode pulumi.StringInput
	accessMode pulumi.StringOutput

	registry pulumi.StringOutput
	chartUrl pulumi.StringOutput
}

func NewRedis(ctx *pulumi.Context, name string, args *RedisArgs, opts ...pulumi.ResourceOption) (*Redis, error) {
	rd := &Redis{}
	args = rd.defaults(args)
	if err := rd.check(args); err != nil {
		return nil, err
	}
	err := ctx.RegisterComponentResource("ctfer-io:ctfer:redis", name, rd, opts...)
	if err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(rd))

	if err := rd.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := rd.outputs(ctx); err != nil {
		return nil, err
	}

	return rd, nil
}

func (rd *Redis) defaults(args *RedisArgs) *RedisArgs {
	if args == nil {
		args = &RedisArgs{}
	}

	args.chartUrl = pulumi.String(defaultRedisChartURL).ToStringOutput()
	if args.ChartsRepository != nil {
		args.chartUrl = args.ChartsRepository.ToStringOutput().ApplyT(func(chartRepository string) string {
			if chartRepository == "" {
				return defaultRedisChartURL
			}
			return fmt.Sprintf("%s/redis", chartRepository)
		}).(pulumi.StringOutput)
	}

	// Define private registry if any
	args.registry = pulumi.String("").ToStringOutput()
	if args.Registry != nil {
		args.registry = args.Registry.ToStringPtrOutput().ApplyT(func(in *string) string {
			// No private registry -> defaults to Docker Hub
			if in == nil {
				return ""
			}

			str := *in
			// If one set, make sure it ends with one '/'
			if str != "" && !strings.HasSuffix(str, "/") {
				str = str + "/"
			}
			return str
		}).(pulumi.StringOutput)
	}

	// Don't default storage class name -> will select the default one
	// on the K8s cluster.
	if args.StorageClassName != nil {
		args.storageClassName = args.StorageClassName.ToStringOutput().ApplyT(func(scm string) *string {
			if scm == "" {
				return nil
			}
			return &scm
		}).(pulumi.StringPtrOutput)
	}

	// The accessModes cannot be empty on a PVC, use ReadWriteOnce
	args.accessMode = pulumi.String(defaultAccessMode).ToStringOutput()
	if args.AccessMode != nil {
		args.accessMode = args.AccessMode.ToStringOutput().ApplyT(func(am string) string {
			if am == "" {
				return defaultAccessMode
			}
			return am
		}).(pulumi.StringOutput)
	}

	return args
}

func (rd *Redis) check(_ *RedisArgs) error {
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

func (rd *Redis) provision(ctx *pulumi.Context, args *RedisArgs, opts ...pulumi.ResourceOption) (err error) {
	// => Secret
	rd.pass, err = random.NewRandomPassword(ctx, "redis-pass", &random.RandomPasswordArgs{
		Length:  pulumi.Int(64),
		Special: pulumi.BoolPtr(false),
	}, opts...)
	if err != nil {
		return
	}

	rd.sec, err = corev1.NewSecret(ctx, "redis-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("redis"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"redis-password": rd.pass.Result,
		}),
	}, opts...)
	if err != nil {
		return
	}

	rd.chart, err = helmv4.NewChart(ctx, "redis", &helmv4.ChartArgs{
		Namespace: args.Namespace,
		Version:   pulumi.String("20.13.4"),
		Chart:     args.chartUrl,
		Values: pulumi.Map{
			"global": args.registry.ToStringOutput().ApplyT(func(repo string) map[string]any {
				mp := map[string]any{}

				// Enable pulling images from private registry
				if repo != "" {
					mp["imageRegistry"] = repo[:len(repo)-1]
					mp["security"] = map[string]any{
						"allowInsecureImages": true,
					}
				}
				return mp
			}).(pulumi.MapOutput),
			"auth": pulumi.Map{
				"existingSecret": rd.sec.Metadata.Name(), // use secret with generated passwords above
			},
			"master": pulumi.Map{
				"persistence": pulumi.Map{
					"storageClass": args.storageClassName,
					"accessModes": pulumi.StringArray{
						args.accessMode,
					},
				},
				// Taint-Based Eviction
				"tolerations": pulumi.MapArray{
					pulumi.Map{
						"key":               pulumi.String("node.kubernetes.io/not-ready"),
						"operator":          pulumi.String("Exists"),
						"effect":            pulumi.String("NoExecute"),
						"tolerationSeconds": pulumi.Int(30),
					},
					pulumi.Map{
						"key":               pulumi.String("node.kubernetes.io/unreachable"),
						"operator":          pulumi.String("Exists"),
						"effect":            pulumi.String("NoExecute"),
						"tolerationSeconds": pulumi.Int(30),
					},
				},
			},
			"architecture": pulumi.String("standalone"), // we don't use replicas for RO actions, TODO enable sentinel
			"networkPolicy": pulumi.Map{
				"allowExternal":       pulumi.Bool(false),
				"allowExternalEgress": pulumi.Bool(false),
			},
			"commonLabels": pulumi.StringMap{
				"ctfer.io/stack-name": pulumi.String(ctx.Stack()),
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	return
}

func (rd *Redis) outputs(ctx *pulumi.Context) error {
	rd.URL = pulumi.Sprintf("redis://:%s@redis-master:6379", rd.pass.Result)
	rd.PodLabels = pulumi.StringMap{
		"app.kubernetes.io/name": pulumi.String("redis"),
		"ctfer.io/stack-name":    pulumi.String(ctx.Stack()),
	}.ToStringMapOutput()

	return ctx.RegisterResourceOutputs(rd, pulumi.Map{
		"url":       rd.URL,
		"podLabels": rd.PodLabels,
	})
}
