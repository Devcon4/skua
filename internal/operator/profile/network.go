package profile

import (
	"maps"
	"net"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/Devcon4/skua/internal/contract/config"
)

const (
	linkLocal4   = "169.254.0.0/16"
	linkLocal6   = "fe80::/10"
	awsMetadata6 = "fd00:ec2::254/128"
	gceMetadata6 = "fd20:ce::254/128"
)

func networkPolicy(cfg *config.Config) *extv1beta1.NetworkPolicySpec {
	var except4, except6 []string
	for _, group := range [][]string{cfg.Network.PodCIDRs, cfg.Network.ServiceCIDRs, cfg.Network.NodeCIDRs} {
		for _, cidr := range group {
			if ip, _, err := net.ParseCIDR(cidr); err == nil && ip.To4() == nil {
				except6 = append(except6, cidr)
			} else {
				except4 = append(except4, cidr)
			}
		}
	}
	external := []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
		CIDR: "0.0.0.0/0", Except: append([]string{linkLocal4}, except4...),
	}}}
	// An IPv6 rule without the cluster's IPv6 ranges in except would open them, so it is rendered only when they are configured.
	if len(except6) > 0 {
		external = append(external, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{
			CIDR: "::/0", Except: append([]string{linkLocal6, awsMetadata6, gceMetadata6}, except6...),
		}})
	}
	egress := []networkingv1.NetworkPolicyEgressRule{
		{To: external},
		{
			To: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: namespaceSelector("kube-system"),
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
			}},
			Ports: []networkingv1.NetworkPolicyPort{port(corev1.ProtocolUDP, 53), port(corev1.ProtocolTCP, 53)},
		},
		targetRule(cfg.Gateway.Egress),
		targetRule(cfg.API.Egress),
	}
	for _, t := range cfg.Network.ExtraEgress {
		egress = append(egress, targetRule(t))
	}
	return &extv1beta1.NetworkPolicySpec{
		Ingress: []networkingv1.NetworkPolicyIngressRule{},
		Egress:  egress,
	}
}

func targetRule(t config.EgressTarget) networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: namespaceSelector(t.Namespace),
			PodSelector:       &metav1.LabelSelector{MatchLabels: maps.Clone(t.PodLabels)},
		}},
		Ports: []networkingv1.NetworkPolicyPort{port(corev1.ProtocolTCP, t.Port)},
	}
}

func namespaceSelector(name string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: name}}
}

func port(protocol corev1.Protocol, n int32) networkingv1.NetworkPolicyPort {
	return networkingv1.NetworkPolicyPort{Protocol: ptr.To(protocol), Port: ptr.To(intstr.FromInt32(n))}
}
