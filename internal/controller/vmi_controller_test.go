package controller

import (
	"testing"

	kubevirtv1 "kubevirt.io/api/core/v1"

	dnsendpointv1alpha1 "sigs.k8s.io/external-dns/endpoint"
)

// ---------- extractMultusIPs ----------

func TestExtractMultusIPs_EmptyInterfaces(t *testing.T) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	v4, v6 := extractMultusIPs(vmi)
	if len(v4) != 0 || len(v6) != 0 {
		t.Errorf("expected no IPs, got v4=%v v6=%v", v4, v6)
	}
}

func TestExtractMultusIPs_OnlyNonMultusSource(t *testing.T) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{
		{IP: "10.0.0.1", InfoSource: "domain"},
		{IP: "10.0.0.2", InfoSource: "guest-agent"},
	}
	v4, v6 := extractMultusIPs(vmi)
	if len(v4) != 0 || len(v6) != 0 {
		t.Errorf("expected no IPs, got v4=%v v6=%v", v4, v6)
	}
}

func TestExtractMultusIPs_MultusSourceIPv4(t *testing.T) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{
		{IP: "192.168.1.10", InfoSource: "multus-status"},
	}
	v4, v6 := extractMultusIPs(vmi)
	if len(v4) != 1 || v4[0] != "192.168.1.10" {
		t.Errorf("expected [192.168.1.10], got v4=%v", v4)
	}
	if len(v6) != 0 {
		t.Errorf("expected no IPv6 addresses, got %v", v6)
	}
}

func TestExtractMultusIPs_MultusSourceIPv6(t *testing.T) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{
		{IP: "2001:db8::1", InfoSource: "multus-status"},
	}
	v4, v6 := extractMultusIPs(vmi)
	if len(v4) != 0 {
		t.Errorf("expected no IPv4 addresses, got %v", v4)
	}
	if len(v6) != 1 || v6[0] != "2001:db8::1" {
		t.Errorf("expected [2001:db8::1], got v6=%v", v6)
	}
}

func TestExtractMultusIPs_Mixed(t *testing.T) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{
		{IP: "192.168.1.10", InfoSource: "multus-status"},
		{IP: "10.0.0.5", InfoSource: "domain"}, // should be skipped
		{IP: "2001:db8::1", InfoSource: "multus-status"},
		{IP: "", InfoSource: "multus-status"}, // empty IP, should be skipped
	}
	v4, v6 := extractMultusIPs(vmi)
	if len(v4) != 1 || v4[0] != "192.168.1.10" {
		t.Errorf("expected v4=[192.168.1.10], got %v", v4)
	}
	if len(v6) != 1 || v6[0] != "2001:db8::1" {
		t.Errorf("expected v6=[2001:db8::1], got %v", v6)
	}
}

func TestExtractMultusIPs_CommaSeperatedInfoSource(t *testing.T) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmi.Status.Interfaces = []kubevirtv1.VirtualMachineInstanceNetworkInterface{
		{IP: "10.10.10.10", InfoSource: "domain,multus-status"},
	}
	v4, _ := extractMultusIPs(vmi)
	if len(v4) != 1 || v4[0] != "10.10.10.10" {
		t.Errorf("expected [10.10.10.10], got %v", v4)
	}
}

// ---------- containsMultusSource ----------

func TestContainsMultusSource(t *testing.T) {
	tests := []struct {
		infoSource string
		want       bool
	}{
		{"multus-status", true},
		{"domain,multus-status", true},
		{"multus-status,guest-agent", true},
		{"domain", false},
		{"guest-agent", false},
		{"", false},
		{"multus", false},
	}
	for _, tt := range tests {
		got := containsMultusSource(tt.infoSource)
		if got != tt.want {
			t.Errorf("containsMultusSource(%q) = %v, want %v", tt.infoSource, got, tt.want)
		}
	}
}

// ---------- parseHostnames ----------

func TestParseHostnames(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"foo.example.com", []string{"foo.example.com"}},
		{"foo.example.com,bar.example.com", []string{"foo.example.com", "bar.example.com"}},
		{"  foo.example.com , bar.example.com  ", []string{"foo.example.com", "bar.example.com"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := parseHostnames(tt.raw)
		if len(got) != len(tt.want) {
			t.Errorf("parseHostnames(%q) = %v, want %v", tt.raw, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseHostnames(%q)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
			}
		}
	}
}

// ---------- parseTTL ----------

func TestParseTTL(t *testing.T) {
	tests := []struct {
		raw  string
		want dnsendpointv1alpha1.TTL
	}{
		{"", defaultTTL},
		{"300", 300},
		{"60", 60},
		{"abc", defaultTTL},
		{"-1", defaultTTL},
		{"0", defaultTTL},
	}
	for _, tt := range tests {
		got := parseTTL(tt.raw)
		if got != tt.want {
			t.Errorf("parseTTL(%q) = %d, want %d", tt.raw, got, tt.want)
		}
	}
}

// ---------- buildEndpoints ----------

func TestBuildEndpoints_BothRecordTypes(t *testing.T) {
	hostnames := []string{"vm.example.com"}
	ipv4 := []string{"192.168.1.1"}
	ipv6 := []string{"2001:db8::1"}
	ttl := dnsendpointv1alpha1.TTL(300)

	eps := buildEndpoints(hostnames, ipv4, ipv6, ttl)
	if len(eps) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(eps))
	}

	aEp := eps[0]
	if aEp.RecordType != "A" {
		t.Errorf("expected first endpoint RecordType=A, got %s", aEp.RecordType)
	}
	if len(aEp.Targets) != 1 || aEp.Targets[0] != "192.168.1.1" {
		t.Errorf("unexpected A targets: %v", aEp.Targets)
	}

	aaaaEp := eps[1]
	if aaaaEp.RecordType != "AAAA" {
		t.Errorf("expected second endpoint RecordType=AAAA, got %s", aaaaEp.RecordType)
	}
	if len(aaaaEp.Targets) != 1 || aaaaEp.Targets[0] != "2001:db8::1" {
		t.Errorf("unexpected AAAA targets: %v", aaaaEp.Targets)
	}
}

func TestBuildEndpoints_OnlyIPv4(t *testing.T) {
	eps := buildEndpoints([]string{"vm.example.com"}, []string{"10.0.0.1"}, nil, defaultTTL)
	if len(eps) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(eps))
	}
	if eps[0].RecordType != "A" {
		t.Errorf("expected RecordType=A, got %s", eps[0].RecordType)
	}
}

func TestBuildEndpoints_OnlyIPv6(t *testing.T) {
	eps := buildEndpoints([]string{"vm.example.com"}, nil, []string{"::1"}, defaultTTL)
	if len(eps) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(eps))
	}
	if eps[0].RecordType != "AAAA" {
		t.Errorf("expected RecordType=AAAA, got %s", eps[0].RecordType)
	}
}

func TestBuildEndpoints_MultipleHostnames(t *testing.T) {
	hostnames := []string{"vm.example.com", "vm2.example.com"}
	ipv4 := []string{"10.0.0.1"}
	eps := buildEndpoints(hostnames, ipv4, nil, defaultTTL)
	// 1 A record per hostname
	if len(eps) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(eps))
	}
	if eps[0].DNSName != "vm.example.com" {
		t.Errorf("unexpected DNSName: %s", eps[0].DNSName)
	}
	if eps[1].DNSName != "vm2.example.com" {
		t.Errorf("unexpected DNSName: %s", eps[1].DNSName)
	}
}

func TestBuildEndpoints_TTL(t *testing.T) {
	eps := buildEndpoints([]string{"vm.example.com"}, []string{"1.2.3.4"}, nil, 120)
	if len(eps) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(eps))
	}
	if eps[0].RecordTTL != 120 {
		t.Errorf("expected TTL=120, got %d", eps[0].RecordTTL)
	}
}
