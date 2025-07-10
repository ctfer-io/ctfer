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
	defaultMdbChartURL = "oci://registry-1.docker.io/bitnamicharts/mariadb"
)

type MariaDB struct {
	pulumi.ResourceState

	masterPass *random.RandomPassword
	userName   pulumi.StringOutput
	userPass   *random.RandomPassword
	repPass    *random.RandomPassword
	sec        *corev1.Secret
	chart      *helmv4.Chart

	// SecretName that points to a Secret with a k
	SecretName pulumi.StringOutput
}

type MariaDBArgs struct {
	Namespace        pulumi.StringInput
	ChartsRepository pulumi.StringInput
	ChartVersion     pulumi.StringInput
	Registry         pulumi.StringInput

	registry pulumi.StringOutput
	chartUrl pulumi.StringOutput
}

// NewMariaDB creates a HA MariaDB cluster.
func NewMariaDB(ctx *pulumi.Context, name string, args *MariaDBArgs, opts ...pulumi.ResourceOption) (*MariaDB, error) {
	mdb := &MariaDB{}
	args = mdb.defaults(args)
	if err := mdb.check(args); err != nil {
		return nil, err
	}
	err := ctx.RegisterComponentResource("ctfer-io:ctfer:mariadb", name, mdb, opts...)
	if err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(mdb))

	if err := mdb.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := mdb.outputs(ctx); err != nil {
		return nil, err
	}

	return mdb, nil
}

func (mdb *MariaDB) defaults(args *MariaDBArgs) *MariaDBArgs {
	if args == nil {
		args = &MariaDBArgs{}
	}

	args.chartUrl = pulumi.String(defaultMdbChartURL).ToStringOutput()
	if args.ChartsRepository != nil {
		args.chartUrl = args.ChartsRepository.ToStringOutput().ApplyT(func(chartRepository string) string {
			if chartRepository == "" {
				return defaultMdbChartURL
			}
			return fmt.Sprintf("%s/mariadb", chartRepository)
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

	return args
}

func (mdb *MariaDB) check(_ *MariaDBArgs) error {
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

func (mdb *MariaDB) provision(ctx *pulumi.Context, args *MariaDBArgs, opts ...pulumi.ResourceOption) (err error) {
	mariadbLabels := pulumi.StringMap{
		"ctfer.io/app-name": pulumi.String("mariadb"),
		"ctfer.io/part-of":  pulumi.String("ctfer"),
	}

	// => Secrets
	mdb.masterPass, err = random.NewRandomPassword(ctx, "masterPass-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, opts...)
	if err != nil {
		return
	}

	mdb.userName = pulumi.String("ctfer").ToStringOutput()
	mdb.userPass, err = random.NewRandomPassword(ctx, "userPass-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, opts...)
	if err != nil {
		return
	}

	mdb.repPass, err = random.NewRandomPassword(ctx, "replication-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, opts...)
	if err != nil {
		return
	}

	mdb.sec, err = corev1.NewSecret(ctx, "mariadb-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    mariadbLabels,
			Name:      pulumi.String("mariadb-secret"),
			Namespace: args.Namespace,
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"mariadb-root-password":        mdb.masterPass.Result,
			"mariadb-password":             mdb.userPass.Result,
			"mariadb-replication-password": mdb.repPass.Result,
			"mariadb-url":                  pulumi.Sprintf("mysql+pymysql://%s:%s@mariadb-headless/ctfd", mdb.userName, mdb.userPass.Result),
		}),
	}, opts...)
	if err != nil {
		return
	}

	mdb.chart, err = helmv4.NewChart(ctx, "mariadb", &helmv4.ChartArgs{
		Namespace: args.Namespace,
		Version:   args.ChartVersion,
		Chart:     args.chartUrl,
		Values: pulumi.Map{
			"global": args.registry.ToStringOutput().ApplyT(func(repo string) map[string]any {
				mp := map[string]any{}

				// Enable pulling images from private registry
				if repo != "" {
					mp["imageRegistry"] = repo
					mp["security"] = map[string]any{
						"allowInsecureImages": true,
					}
				}
				return mp
			}).(pulumi.MapOutput),
			"auth": pulumi.Map{
				"username":       mdb.userName,
				"database":       pulumi.String("ctfd"),
				"existingSecret": mdb.sec.Metadata.Name(), // use secret with generated passwords above
			},
			"primary": pulumi.Map{
				"podLabels": mariadbLabels,
				"persistence": pulumi.Map{
					"storageClass": pulumi.String("longhorn"),
					"accessModes": pulumi.StringArray{
						pulumi.String("ReadWriteMany"),
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
			"architecture": pulumi.String("standalone"), // explicit
		},
	}, opts...)
	if err != nil {
		return
	}

	return
}

func (mdb *MariaDB) outputs(ctx *pulumi.Context) error {
	mdb.SecretName = mdb.sec.Metadata.Name().Elem()

	return ctx.RegisterResourceOutputs(mdb, pulumi.Map{
		"secretName": mdb.SecretName,
	})
}
