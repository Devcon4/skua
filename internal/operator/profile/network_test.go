package profile

import (
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/Devcon4/skua/internal/contract/config"
)

func TestNetworkPolicy(t *testing.T) {
	cfg := testConfig()
	np := Template(testProfile(), cfg).Spec.NetworkPolicy
	if np == nil {
		t.Fatal("networkPolicy unset")
	}
	if len(np.Ingress) != 0 {
		t.Errorf("ingress = %+v, want none", np.Ingress)
	}
	if len(np.Egress) != 5 {
		t.Fatalf("egress rules = %d, want 5: %+v", len(np.Egress), np.Egress)
	}
	external := np.Egress[0]
	if len(external.To) != 1 || external.To[0].IPBlock == nil || external.To[0].IPBlock.CIDR != "0.0.0.0/0" || len(external.Ports) != 0 {
		t.Fatalf("external rule = %+v", external)
	}
	except := external.To[0].IPBlock.Except
	for _, cidr := range slices.Concat([]string{"169.254.0.0/16"}, cfg.Network.PodCIDRs, cfg.Network.ServiceCIDRs, cfg.Network.NodeCIDRs) {
		if !slices.Contains(except, cidr) {
			t.Errorf("except = %v, missing %s", except, cidr)
		}
	}

	dns := np.Egress[1]
	if len(dns.To) != 1 || dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
		t.Errorf("dns rule = %+v", dns)
	}
	var dnsPorts []string
	for _, p := range dns.Ports {
		dnsPorts = append(dnsPorts, string(*p.Protocol)+"/"+p.Port.String())
	}
	if !reflect.DeepEqual(dnsPorts, []string{"UDP/53", "TCP/53"}) {
		t.Errorf("dns ports = %v", dnsPorts)
	}

	targets := []config.EgressTarget{cfg.Gateway.Egress, cfg.API.Egress, cfg.Network.ExtraEgress[0]}
	for i, target := range targets {
		assertTargetRule(t, np.Egress[2+i], target)
	}
}

func assertTargetRule(t *testing.T, rule networkingv1.NetworkPolicyEgressRule, target config.EgressTarget) {
	t.Helper()
	if len(rule.To) != 1 || rule.To[0].IPBlock != nil {
		t.Fatalf("rule for %s = %+v", target.Namespace, rule)
	}
	peer := rule.To[0]
	if !reflect.DeepEqual(peer.NamespaceSelector.MatchLabels, map[string]string{"kubernetes.io/metadata.name": target.Namespace}) {
		t.Errorf("namespace selector = %+v, want %s", peer.NamespaceSelector, target.Namespace)
	}
	if peer.PodSelector == nil || !reflect.DeepEqual(peer.PodSelector.MatchLabels, target.PodLabels) {
		t.Errorf("pod selector = %+v, want %v", peer.PodSelector, target.PodLabels)
	}
	if len(rule.Ports) != 1 || *rule.Ports[0].Protocol != corev1.ProtocolTCP || rule.Ports[0].Port.IntVal != target.Port {
		t.Errorf("ports = %+v, want TCP/%d", rule.Ports, target.Port)
	}
}

func TestNetworkPolicyIPv6Ranges(t *testing.T) {
	cfg := testConfig()
	cfg.Network.PodCIDRs = append(cfg.Network.PodCIDRs, "fd00:10:244::/56")
	np := Template(testProfile(), cfg).Spec.NetworkPolicy
	peers := np.Egress[0].To
	want6 := []string{"fe80::/10", "fd00:ec2::254/128", "fd20:ce::254/128", "fd00:10:244::/56"}
	if len(peers) != 2 || peers[1].IPBlock.CIDR != "::/0" || !reflect.DeepEqual(peers[1].IPBlock.Except, want6) {
		t.Fatalf("external peers = %+v", peers)
	}
	if slices.Contains(peers[0].IPBlock.Except, "fd00:10:244::/56") {
		t.Errorf("IPv4 except holds an IPv6 range: %v", peers[0].IPBlock.Except)
	}
}
