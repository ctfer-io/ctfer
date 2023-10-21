package components

import (
	"fmt"

	"github.com/ctfer-io/ctfer/internal"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Redis struct {
	pulumi.ResourceState

	URL pulumi.StringOutput
}

var _ pulumi.ComponentResource = (*Redis)(nil)

func NewRedis(ctx *pulumi.Context, name string, args *RedisArgs, opts ...pulumi.ResourceOption) (*Redis, error) {
	rd := &Redis{}

	// Register the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:Redis", name, rd, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources
	redisLabels := pulumi.StringMap{
		"ctfer/infra": pulumi.String("redis"),
	}
	// => ConfigMap
	_, err = corev1.NewConfigMap(ctx, "redis-configmap", &corev1.ConfigMapArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("redis-configmap"),
			Labels:    redisLabels,
			Namespace: args.Namespace,
		},
		Data: pulumi.StringMap{
			"update-node.sh": pulumi.String(`#!/bin/sh
REDIS_NODES="/data/nodes.conf"
sed -i -e "/myself/ s/[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}/${POD_IP}/" ${REDIS_NODES}
exec "$@"`),
			"redis.conf": pulumi.String(`cluster-enabled yes
cluster-require-full-coverage no
cluster-node-timeout 15000
cluster-config-file /data/nodes.conf
cluster-migration-barrier 1
appendonly yes
protected-mode no`),
		},
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}

	// => Secret
	redisPass, err := random.NewRandomPassword(ctx, "redis-pass", &random.RandomPasswordArgs{
		Length:  pulumi.Int(64),
		Special: pulumi.BoolPtr(false),
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}
	rd.URL = redisPass.Result.ApplyT(func(pass string) string {
		return fmt.Sprintf("redis://:%s@redis-svc:6379", pass)
	}).(pulumi.StringOutput)
	_, err = corev1.NewSecret(ctx, "redis-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    redisLabels,
			Name:      pulumi.String("redis-secret"),
			Namespace: args.Namespace,
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"redis-password": redisPass.Result,
			"redis-url":      rd.URL,
		}),
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}

	// => Service
	_, err = corev1.NewService(ctx, "redis-svc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("redis-svc"),
			Labels:    redisLabels,
			Namespace: args.Namespace,
		},
		Spec: corev1.ServiceSpecArgs{
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Port:       pulumi.Int(6379),
					TargetPort: pulumi.Int(6379),
					Name:       pulumi.String("client"),
				},
			},
			// Headless, for DNS purposes
			ClusterIP: pulumi.String("None"),
			Selector:  redisLabels,
		},
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}

	// => StatefulSet
	_, err = appsv1.NewStatefulSet(ctx, "redis-sts", &appsv1.StatefulSetArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("redis-sts"),
			Labels:    redisLabels,
			Namespace: args.Namespace,
		},
		Spec: appsv1.StatefulSetSpecArgs{
			ServiceName: pulumi.String("redis-svc"),
			Replicas:    args.Replicas,
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: redisLabels,
			},
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels:    redisLabels,
				},
				Spec: corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("redis"),
							Image: pulumi.String(internal.GetImage("redis:7.0.10")),
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									ContainerPort: pulumi.Int(6379),
									Name:          pulumi.String("client"),
								},
							},
							Args: pulumi.ToStringArray([]string{
								"--requirepass",
								"$(REDIS_PASSWORD)",
							}),
							Env: corev1.EnvVarArray{
								corev1.EnvVarArgs{
									Name: pulumi.String("REDIS_PASSWORD"),
									ValueFrom: corev1.EnvVarSourceArgs{
										SecretKeyRef: corev1.SecretKeySelectorArgs{
											Name: pulumi.String("redis-secret"),
											Key:  pulumi.String("redis-password"),
										},
									},
								},
							},
							VolumeMounts: corev1.VolumeMountArray{
								corev1.VolumeMountArgs{
									Name:      pulumi.String("conf"),
									MountPath: pulumi.String("/conf"),
									ReadOnly:  pulumi.Bool(false),
								},
								corev1.VolumeMountArgs{
									Name:      pulumi.String("data"),
									MountPath: pulumi.String("/data"),
									ReadOnly:  pulumi.Bool(false),
								},
							},
						},
					},
					Volumes: corev1.VolumeArray{
						corev1.VolumeArgs{
							Name: pulumi.String("conf"),
							ConfigMap: corev1.ConfigMapVolumeSourceArgs{
								Name:        pulumi.String("redis-configmap"),
								DefaultMode: pulumi.Int(0755),
							},
						},
					},
				},
			},
			VolumeClaimTemplates: corev1.PersistentVolumeClaimTypeArray{
				corev1.PersistentVolumeClaimTypeArgs{
					Metadata: metav1.ObjectMetaArgs{
						Name: pulumi.String("data"),
					},
					Spec: corev1.PersistentVolumeClaimSpecArgs{
						AccessModes: pulumi.ToStringArray([]string{
							"ReadWriteOnce",
						}),
						Resources: corev1.ResourceRequirementsArgs{
							Requests: pulumi.ToStringMap(map[string]string{
								"storage": "1Gi",
							}),
						},
					},
				},
			},
		},
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}

	return rd, nil
}

type RedisArgs struct {
	// Namespace to deploy to.
	Namespace pulumi.String
	// Replicas is the number of secondary replicas to run.
	Replicas pulumi.Int
}
