package components

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv4 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v4"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	yamlv2 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml/v2"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.uber.org/multierr"
)

const (
	defaultRedisChartURL            = "oci://registry-1.docker.io/bitnamicharts/redis"
	defaultRedisToApiServerTemplate = `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: cilium-redis-to-apiserver-allow-{{ .Stack }}
  namespace: {{ .Namespace }}
spec:
  endpointSelector:
    matchLabels:
    {{- range $k, $v := .PodLabels }}
      {{ $k }}: {{ $v }}
    {{- end }}
  egress:
  - toEntities:
    - kube-apiserver
  - toPorts:
    - ports:
      - port: "6443"
        protocol: TCP
`
)

type Redis struct {
	pulumi.ResourceState

	pass       *random.RandomPassword
	sec        *corev1.Secret
	chart      *helmv4.Chart
	redisToApi *yamlv2.ConfigGroup

	podLabels pulumi.StringMapInput

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

	// PVCAccessModes defines the access modes supported by the PVC.
	PVCAccessModes pulumi.StringArrayInput
	pvcAccessModes pulumi.StringArrayOutput

	// RedisToApiServerTemplate is a Go text/template that defines the NetworkPolicy
	// YAML schema to use.
	// If none set, it is defaulted to a cilium.io/v2 CiliumNetworkPolicy.
	RedisToApiServerTemplate pulumi.StringPtrInput
	redisToApiServerTemplate pulumi.StringOutput

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
	args.pvcAccessModes = pulumi.ToStringArray(defaultPVCAccessModes).ToStringArrayOutput()
	if args.PVCAccessModes != nil {
		args.pvcAccessModes = args.PVCAccessModes.ToStringArrayOutput().ApplyT(func(am []string) []string {
			if len(am) == 0 {
				return defaultPVCAccessModes
			}
			return am
		}).(pulumi.StringArrayOutput)
	}

	args.redisToApiServerTemplate = pulumi.String(defaultRedisToApiServerTemplate).ToStringOutput()
	if args.RedisToApiServerTemplate != nil {
		args.redisToApiServerTemplate = args.RedisToApiServerTemplate.ToStringPtrOutput().ApplyT(func(redisToApiServerTemplate *string) string {
			if redisToApiServerTemplate == nil || *redisToApiServerTemplate == "" {
				return defaultRedisToApiServerTemplate
			}
			return *redisToApiServerTemplate
		}).(pulumi.StringOutput)
	}

	return args
}

func (rd *Redis) check(args *RedisArgs) error {
	checks := 1
	wg := &sync.WaitGroup{}
	wg.Add(checks)
	cerr := make(chan error, checks)

	// Verify the template is syntactically valid.
	args.redisToApiServerTemplate.ApplyT(func(redisToApiServerTemplate string) error {
		defer wg.Done()

		_, err := template.New("redis-to-apiserver").
			Funcs(sprig.FuncMap()).
			Parse(redisToApiServerTemplate)
		cerr <- err
		return nil
	})
	wg.Wait()
	close(cerr)

	var merr error
	for err := range cerr {
		merr = multierr.Append(merr, err)
	}
	return merr
}

func (rd *Redis) provision(ctx *pulumi.Context, args *RedisArgs, opts ...pulumi.ResourceOption) (err error) {
	rd.podLabels = pulumi.StringMap{
		// "app.kubernetes.io/component": pulumi.String("redis"), // do not use component, already used inside helm chart
		"app.kubernetes.io/part-of": pulumi.String("ctfer"),
		"ctfer.io/stack-name":       pulumi.String(ctx.Stack()),
	}

	// redis to kube-apiserver
	rd.redisToApi, err = yamlv2.NewConfigGroup(ctx, "redis-to-kube-apiserver-netpol", &yamlv2.ConfigGroupArgs{
		Yaml: pulumi.All(args.redisToApiServerTemplate, args.Namespace, rd.podLabels).ApplyT(func(all []any) (string, error) {
			redisToApiServerTemplate := all[0].(string)
			namespace := all[1].(string)
			podLabels := all[2].(map[string]string)

			tmpl, _ := template.New("redis-to-apiserver").
				Funcs(sprig.FuncMap()).
				Parse(redisToApiServerTemplate)

			buf := &bytes.Buffer{}
			if err := tmpl.Execute(buf, map[string]any{
				"Stack":     ctx.Stack(),
				"Namespace": namespace,
				"PodLabels": podLabels,
			}); err != nil {
				return "", err
			}
			return buf.String(), nil
		}).(pulumi.StringOutput),
	}, opts...)
	if err != nil {
		return
	}

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
			Labels:    rd.podLabels,
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
				}
				// XXX the following is required per deprecation notice of bitnami free images.
				// See https://github.com/bitnami/containers/issues/83267 for more info...
				mp["security"] = map[string]any{
					"allowInsecureImages": true,
				}
				return mp
			}).(pulumi.MapOutput),
			"auth": pulumi.Map{
				"existingSecret": rd.sec.Metadata.Name(), // use secret with generated passwords above
			},
			"master": pulumi.Map{
				"persistence": pulumi.Map{
					"storageClass": args.storageClassName,
					"accessModes":  args.pvcAccessModes,
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
				"resources": pulumi.Map{
					"requests": pulumi.Map{
						"cpu":    pulumi.String("250m"),
						"memory": pulumi.String("256Mi"),
					},
					"limits": pulumi.Map{
						"cpu":    pulumi.String("500m"),
						"memory": pulumi.String("512Mi"),
					},
				},
			},
			"architecture": pulumi.String("replication"),
			"replica": pulumi.Map{
				"replicaCount": pulumi.Int(3),
				"persistence": pulumi.Map{
					"storageClass": args.storageClassName,
					"accessModes":  args.pvcAccessModes,
				},
				"automountServiceAccountToken": pulumi.Bool(true),
				"resources": pulumi.Map{
					"requests": pulumi.Map{
						"cpu":    pulumi.String("250m"),
						"memory": pulumi.String("256Mi"),
					},
					"limits": pulumi.Map{
						"cpu":    pulumi.String("500m"),
						"memory": pulumi.String("512Mi"),
					},
				},
			},
			// Sentinel monitor master instance and promote replica into leader if master fails
			"sentinel": pulumi.Map{
				"enabled": pulumi.Bool(true), // enable sentinel container beside redis main container
				"image": pulumi.StringMap{
					// XXX the following is required per deprecation notice of bitnami free images.
					// See https://github.com/bitnami/containers/issues/83267 for more info...
					"repository": pulumi.String("bitnamilegacy/redis-sentinel"),
				},
				"persistence": pulumi.Map{
					"enabled":      pulumi.Bool(true),
					"storageClass": args.storageClassName,
					"accessModes":  args.pvcAccessModes,
				},
				"masterService": pulumi.Map{
					"enabled": pulumi.Bool(true), // create a service that route traffic on master only
				},
				"resources": pulumi.Map{
					"requests": pulumi.Map{
						"cpu":    pulumi.String("100m"),
						"memory": pulumi.String("128Mi"),
					},
					"limits": pulumi.Map{
						"cpu":    pulumi.String("200m"),
						"memory": pulumi.String("256Mi"),
					},
				},
			},
			// image used by sentinel to define label on master pod
			// that route traffic on master
			"kubectl": pulumi.Map{
				"image": pulumi.Map{
					// XXX the following is required per deprecation notice of bitnami free images.
					// See https://github.com/bitnami/containers/issues/83267 for more info...
					"repository": pulumi.String("bitnamilegacy/kubectl"),
				},
			},
			// used by sentinel to edit labels
			"rbac": pulumi.Map{
				"create": pulumi.Bool(true),
			},
			"networkPolicy": pulumi.Map{
				"allowExternal":       pulumi.Bool(false), // only in the same ns
				"allowExternalEgress": pulumi.Bool(false), // don't go outside ns
			},
			"commonLabels": rd.podLabels,
			// XXX the following is required per deprecation notice of bitnami free images.
			// See https://github.com/bitnami/containers/issues/83267 for more info...
			"image": pulumi.StringMap{
				"repository": pulumi.String("bitnamilegacy/redis"),
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
	rd.PodLabels = rd.podLabels.ToStringMapOutput()

	return ctx.RegisterResourceOutputs(rd, pulumi.Map{
		"url":       rd.URL,
		"podLabels": rd.PodLabels,
	})
}
