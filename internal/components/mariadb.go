package components

import (
	"fmt"

	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type MariaDB struct {
	pulumi.ResourceState

	URL pulumi.StringOutput
}

var _ pulumi.ComponentResource = (*MariaDB)(nil)

// NewMariaDB creates a HA MariaDB cluster.
func NewMariaDB(ctx *pulumi.Context, name string, args *MariaDBArgs, opts ...pulumi.ResourceOption) (*MariaDB, error) {
	mdb := &MariaDB{}

	// Register the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:MariaDB", name, mdb, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources
	mariadbLabels := pulumi.StringMap{
		"ctfer/infra": pulumi.String("mariadb"),
	}

	// => Secret
	masterUser := pulumi.String("ctfer")
	masterPass, err := random.NewRandomPassword(ctx, "mariadb-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(64),
		Special: pulumi.BoolPtr(false),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}
	mdb.URL = pulumi.All(masterUser, masterPass.Result).ApplyT(func(args []any) string {
		user := args[0].(string)
		pass := args[1].(string)
		return fmt.Sprintf("mysql+pymysql://%s:%s@mariadb-svc/ctfd", user, pass)
	}).(pulumi.StringOutput)
	_, err = corev1.NewSecret(ctx, "mariadb-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    mariadbLabels,
			Name:      pulumi.String("mariadb-secret"),
			Namespace: args.Namespace,
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"mariadb-root-password": masterPass.Result,
			"mariadb-url":           mdb.URL,
		}),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	// => ConfigMap
	_, err = corev1.NewConfigMap(ctx, "mariadb-configmap", &corev1.ConfigMapArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("mariadb-configmap"),
			Labels:    mariadbLabels,
			Namespace: args.Namespace,
		},
		Data: pulumi.StringMap{
			"primary.cnf": pulumi.String(`[mariadb]
log-bin
log-basename=ctfer-mariadb
`),
			"replica.cnf": pulumi.String(`[mariadb]
log-basename=ctfer-mariadb
`),
			"primary.sql": pulumi.All(masterUser, masterPass.Result).ApplyT(func(args []any) string {
				user := args[0].(string)
				pass := args[1].(string)
				return fmt.Sprintf(`
CREATE USER '%s'@'%%' IDENTIFIED BY '%s';
GRANT REPLICATION REPLICA ON *.* TO '%s'@'%%';
CREATE DATABASE ctfd;
GRANT ALL PRIVILEGES ON ctfd.*  TO '%s'@'%%';
`, user, pass, user, user)
			}).(pulumi.StringOutput),
			"secondary.sql": pulumi.All(masterUser, masterPass.Result).ApplyT(func(args []any) string {
				user := args[0].(string)
				pass := args[1].(string)
				// FIXME mariadb 1st pod URL
				return fmt.Sprintf(`
CHANGE MASTER TO 
MASTER_HOST='mariadb-sts-0',
MASTER_USER='%s',
MASTER_PASSWORD='%s',
MASTER_CONNECT_RETRY=10;
`, user, pass)
			}).(pulumi.StringOutput),
		},
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	// => StatefulSet
	_, err = appsv1.NewStatefulSet(ctx, "mariadb-sts", &appsv1.StatefulSetArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("mariadb-sts"),
			Labels:    mariadbLabels,
			Namespace: args.Namespace,
		},
		Spec: appsv1.StatefulSetSpecArgs{
			ServiceName: pulumi.String("mariadb-svc"),
			Replicas:    args.Replicas,
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: mariadbLabels,
			},
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Labels:    mariadbLabels,
					Namespace: args.Namespace,
				},
				Spec: corev1.PodSpecArgs{
					InitContainers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("init-mariadb"),
							Image: pulumi.String("registry.pandatix.dev/mariadb:10.7.8"),
							Command: pulumi.ToStringArray([]string{
								`bash`,
								`-c`,
								`set -ex
echo 'Starting init-mariadb';
# Check config map to directory that already exists 
# (but must be used as a volume for main container)
ls /mnt/config-map
# Statefulset has sticky identity, number should be last
[[ ` + "`hostname`" + ` =~ -([0-9]+)$ ]] || exit 1
ordinal=${BASH_REMATCH[1]}
# Copy appropriate conf.d files from config-map to 
# mariadb-config volume (emptyDir) depending on pod number
if [[ $ordinal -eq 0 ]]; then
	# This file holds SQL for connecting to primary
	cp /mnt/config-map/primary.cnf /etc/mysql/conf.d/server-id.cnf
	# Create the users needed for replication on primary on a volume
	# initdb (emptyDir)
	cp /mnt/config-map/primary.sql /docker-entrypoint-initdb.d
else
	# This file holds SQL for connecting to secondary
	cp /mnt/config-map/replica.cnf /etc/mysql/conf.d/server-id.cnf
	# On replicas use secondary configuration on initdb volume
	cp /mnt/config-map/secondary.sql /docker-entrypoint-initdb.d
fi
# Add an offset to avoid reserved server-id=0 value.
echo server-id=$((3000 + $ordinal)) >> etc/mysql/conf.d/server-id.cnf
ls /etc/mysql/conf.d/
cat /etc/mysql/conf.d/server-id.cnf`,
							}),
							VolumeMounts: corev1.VolumeMountArray{
								corev1.VolumeMountArgs{
									Name:      pulumi.String("mariadb-config-map"),
									MountPath: pulumi.String("/mnt/config-map"),
								},
								corev1.VolumeMountArgs{
									Name:      pulumi.String("mariadb-config"),
									MountPath: pulumi.String("/etc/mysql/conf.d/"),
								},
								corev1.VolumeMountArgs{
									Name:      pulumi.String("initdb"),
									MountPath: pulumi.String("/docker-entrypoint-initdb.d"),
								},
							},
						},
					},
					RestartPolicy: pulumi.String("Always"),
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("mariadb"),
							Image: pulumi.String("registry.pandatix.dev/mariadb:10.7.8"),
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									ContainerPort: pulumi.Int(3306),
									Name:          pulumi.String("mariadb-port"),
								},
							},
							// Using Secrets
							Env: corev1.EnvVarArray{
								corev1.EnvVarArgs{
									Name: pulumi.String("MARIADB_ROOT_PASSWORD"),
									ValueFrom: corev1.EnvVarSourceArgs{
										SecretKeyRef: corev1.SecretKeySelectorArgs{
											Name: pulumi.String("mariadb-secret"),
											Key:  pulumi.String("mariadb-root-password"),
										},
									},
								},
								corev1.EnvVarArgs{
									Name:  pulumi.String("MYSQL_INITDB_SKIP_TZINFO"),
									Value: pulumi.String("1"),
								},
							},
							// Mount volume from persistent volume claim
							VolumeMounts: corev1.VolumeMountArray{
								corev1.VolumeMountArgs{
									Name:      pulumi.String("datadir"),
									MountPath: pulumi.String("/var/lib/mysql/"),
								},
								corev1.VolumeMountArgs{
									Name:      pulumi.String("mariadb-config"),
									MountPath: pulumi.String("/etc/mysql/conf.d/"),
								},
								corev1.VolumeMountArgs{
									Name:      pulumi.String("initdb"),
									MountPath: pulumi.String("/docker-entrypoint-initdb.d"),
								},
							},
						},
					},
					Volumes: corev1.VolumeArray{
						corev1.VolumeArgs{
							Name: pulumi.String("mariadb-config-map"),
							ConfigMap: corev1.ConfigMapVolumeSourceArgs{
								Name: pulumi.String("mariadb-configmap"),
							},
						},
						corev1.VolumeArgs{
							Name:     pulumi.String("mariadb-config"),
							EmptyDir: corev1.EmptyDirVolumeSourceArgs{},
						},
						corev1.VolumeArgs{
							Name:     pulumi.String("initdb"),
							EmptyDir: corev1.EmptyDirVolumeSourceArgs{},
						},
					},
				},
			},
			VolumeClaimTemplates: corev1.PersistentVolumeClaimTypeArray{
				corev1.PersistentVolumeClaimTypeArgs{
					Metadata: metav1.ObjectMetaArgs{
						Name: pulumi.String("datadir"),
					},
					Spec: corev1.PersistentVolumeClaimSpecArgs{
						StorageClassName: pulumi.String("longhorn"),
						AccessModes: pulumi.ToStringArray([]string{
							"ReadWriteOnce",
						}),
						Resources: corev1.ResourceRequirementsArgs{
							Requests: pulumi.ToStringMap(map[string]string{
								"storage": "2Gi",
							}),
						},
					},
				},
			},
		},
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	// => Service
	_, err = corev1.NewService(ctx, "mariadb-svc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("mariadb-svc"),
			Labels:    mariadbLabels,
			Namespace: args.Namespace,
		},
		Spec: corev1.ServiceSpecArgs{
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Port: pulumi.Int(3306),
					Name: pulumi.String("mariadb-port"),
				},
			},
			// Headless, for DNS purposes
			ClusterIP: pulumi.String("None"),
			Selector:  mariadbLabels,
		},
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	return mdb, nil
}

type MariaDBArgs struct {
	// Namespace to deploy to.
	Namespace pulumi.String
	// Replicas is the number of secondary replicas to run.
	Replicas pulumi.Int
}
