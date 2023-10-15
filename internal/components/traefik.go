package components

import (
	"encoding/base64"
	"os"

	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/meta/v1"
	rbacv1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/rbac/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Traefik struct {
	pulumi.ResourceState
}

var _ pulumi.ComponentResource = (*Traefik)(nil)

func NewTraefik(ctx *pulumi.Context, name string, args *TraefikArgs, opts ...pulumi.ResourceOption) (*Traefik, error) {
	tfk := &Traefik{}

	// Regsiter the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:Traefik", name, tfk, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources (https://doc.traefik.io/traefik/getting-started/quick-start-with-kubernetes/)
	traefikLabels := pulumi.StringMap{
		"app": pulumi.String("traefik"),
	}
	// => ClusterRole, used to create a dedicated service acccount for Traefik
	_, err = rbacv1.NewClusterRole(ctx, "traefik-role", &rbacv1.ClusterRoleArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("traefik-role"),
			Namespace: args.Namespace,
			Labels:    traefikLabels,
		},
		Rules: rbacv1.PolicyRuleArray{
			rbacv1.PolicyRuleArgs{
				ApiGroups: pulumi.ToStringArray([]string{
					"",
				}),
				Resources: pulumi.ToStringArray([]string{
					"services",
					"endpoints",
					"secrets",
				}),
				Verbs: pulumi.ToStringArray([]string{
					"get",
					"list",
					"watch",
				}),
			},
			rbacv1.PolicyRuleArgs{
				ApiGroups: pulumi.ToStringArray([]string{
					"extensions",
					"networking.k8s.io",
				}),
				Resources: pulumi.ToStringArray([]string{
					"ingresses",
					"ingressclasses",
				}),
				Verbs: pulumi.ToStringArray([]string{
					"get",
					"list",
					"watch",
				}),
			},
			rbacv1.PolicyRuleArgs{
				ApiGroups: pulumi.ToStringArray([]string{
					"extensions",
					"networking.k8s.io",
				}),
				Resources: pulumi.ToStringArray([]string{
					"ingresses/status",
				}),
				Verbs: pulumi.ToStringArray([]string{
					"update",
				}),
			},
		},
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}

	// => ServiceAccount
	_, err = corev1.NewServiceAccount(ctx, "traefik-account", &corev1.ServiceAccountArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("traefik-account"),
			Namespace: args.Namespace,
			Labels:    traefikLabels,
		},
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}

	// => ClusterRoleBinding, binds the ClusterRole and ServiceAccount
	_, err = rbacv1.NewClusterRoleBinding(ctx, "traefik-role-binding", &rbacv1.ClusterRoleBindingArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("traefik-role-binding"),
			Namespace: args.Namespace,
			Labels:    traefikLabels,
		},
		RoleRef: rbacv1.RoleRefArgs{
			ApiGroup: pulumi.String("rbac.authorization.k8s.io"),
			Kind:     pulumi.String("ClusterRole"),
			Name:     pulumi.String("traefik-role"),
		},
		Subjects: rbacv1.SubjectArray{
			rbacv1.SubjectArgs{
				Kind:      pulumi.String("ServiceAccount"),
				Name:      pulumi.String("traefik-account"),
				Namespace: args.Namespace,
			},
		},
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}

	// => Deployment
	_, err = appsv1.NewDeployment(ctx, "traefik-deployment", &appsv1.DeploymentArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("traefik-deployment"),
			Namespace: args.Namespace,
			Labels:    traefikLabels,
		},
		Spec: appsv1.DeploymentSpecArgs{
			Replicas: pulumi.Int(1),
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: traefikLabels,
			},
			Template: corev1.PodTemplateSpecArgs{
				Metadata: metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels:    traefikLabels,
				},
				Spec: corev1.PodSpecArgs{
					ServiceAccountName: pulumi.String("traefik-account"),
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("traefik"),
							Image: pulumi.String("registry.pandatix.dev/traefik:v2.10.1"),
							Args: pulumi.ToStringArray([]string{
								"--api.insecure", // exposes dashboard on port 8080
								"--providers.kubernetesingress",
								"--entrypoints.web.address=:443",
								"--entrypoints.web.http.tls",
							}),
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									Name:          pulumi.String("web"),
									ContainerPort: pulumi.Int(443),
								},
								corev1.ContainerPortArgs{
									Name:          pulumi.String("dashboard"),
									ContainerPort: pulumi.Int(8080),
								},
							},
						},
					},
				},
			},
		},
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}

	// => Services
	_, err = corev1.NewService(ctx, "traefik-web-service", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("traefik-web-service"),
			Namespace: args.Namespace,
			Labels:    traefikLabels,
		},
		Spec: corev1.ServiceSpecArgs{
			Type: pulumi.String("LoadBalancer"),
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Name: pulumi.String("web"),
					Port: pulumi.Int(443),
				},
			},
			Selector: traefikLabels,
			ExternalIPs: pulumi.ToStringArray([]string{
				"10.50.12.1",
				"10.50.12.2",
				"10.50.12.3",
			}),
		},
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}
	_, err = corev1.NewService(ctx, "traefik-dashboard-service", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("traefik-dashboard-service"),
			Namespace: args.Namespace,
			Labels:    traefikLabels,
		},
		Spec: corev1.ServiceSpecArgs{
			Type: pulumi.String("NodePort"),
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					Name:     pulumi.String("dashboard"),
					Port:     pulumi.Int(8080),
					NodePort: pulumi.Int(30080),
				},
			},
			Selector: traefikLabels,
		},
	}, pulumi.Parent(tfk))
	if err != nil {
		return nil, err
	}

	// Create TLS secret
	// TODO make it configurable
	tlsCrt, err := os.ReadFile("certs/fullchain.pem")
	if err != nil {
		return nil, err
	}
	tlsKey, err := os.ReadFile("certs/privkey.pem")
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
