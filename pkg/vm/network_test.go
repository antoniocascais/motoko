package vm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------- fixtures (mirror real `virsh` output from the target host) ----------

const sampleNetXML = `<network connections='2'>
  <name>default</name>
  <uuid>11111111-2222-3333-4444-555555555555</uuid>
  <forward mode='open'/>
  <bridge name='virbr0' stp='on' delay='0'/>
  <mac address='52:54:00:00:00:00'/>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.122.2' end='192.168.122.254'/>
    </dhcp>
  </ip>
</network>`

const sampleLeases = ` Expiry Time           MAC address         Protocol   IP address           Hostname     Client ID or DUID
--------------------------------------------------------------------------------------------------------------------------------------------------
 2026-05-28 11:04:02   52:54:00:00:00:01   ipv4       192.168.122.177/24   vm-alpha     01:52:54:00:00:00:01
 2026-05-28 11:02:06   52:54:00:00:00:02   ipv4       192.168.122.5/24     vm-beta      01:52:54:00:00:00:02`

const sampleDomiflist = ` Interface   Type      Source    Model    MAC
-------------------------------------------------------
 vnet0       network   default   virtio   52:54:00:ab:cd:ef`

// fakeVirsh routes runCmd calls to canned output keyed by the virsh subcommand.
type fakeVirsh struct {
	dumpXML, leases, netInfo, domiflist          string
	dumpErr, leaseErr, netInfoErr, domiflistErr  error
}

func (f fakeVirsh) run(_ string, args ...string) (string, string, error) {
	for _, a := range args {
		switch a {
		case "net-dumpxml":
			return f.dumpXML, "", f.dumpErr
		case "net-dhcp-leases":
			return f.leases, "", f.leaseErr
		case "net-info":
			return f.netInfo, "", f.netInfoErr
		case "domiflist":
			return f.domiflist, "", f.domiflistErr
		}
	}
	return "", "", nil
}

// leasesWithIPs builds a net-dhcp-leases body marking each given last-octet as used.
func leasesWithIPs(lastOctets ...int) string {
	var b strings.Builder
	b.WriteString(" Expiry Time           MAC address         Protocol   IP address           Hostname\n")
	b.WriteString("------------------------------------------------------------------------------------\n")
	for _, o := range lastOctets {
		fmt.Fprintf(&b, " 2026-05-28 11:00:00   52:54:00:00:00:01   ipv4       192.168.122.%d/24   h\n", o)
	}
	return b.String()
}

// ---------- AllocateIP ----------

func TestAllocateIP_HighEndDownFirstFree(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: sampleNetXML, leases: sampleLeases}.run)

	got, err := AllocateIP("default", "192.168.122.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "192.168.122.254" {
		t.Errorf("AllocateIP = %q, want 192.168.122.254 (top of range)", got)
	}
}

func TestAllocateIP_SkipsStaticReservation(t *testing.T) {
	xml := strings.Replace(sampleNetXML,
		"<range start='192.168.122.2' end='192.168.122.254'/>",
		"<range start='192.168.122.2' end='192.168.122.254'/>\n      <host mac='52:54:00:11:22:33' name='motoko-x' ip='192.168.122.254'/>",
		1)
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: xml, leases: sampleLeases}.run)

	got, err := AllocateIP("default", "192.168.122.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "192.168.122.253" {
		t.Errorf("AllocateIP = %q, want 192.168.122.253 (.254 reserved)", got)
	}
}

func TestAllocateIP_SkipsLeasedAddresses(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: sampleNetXML, leases: leasesWithIPs(254, 253)}.run)

	got, err := AllocateIP("default", "192.168.122.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "192.168.122.252" {
		t.Errorf("AllocateIP = %q, want 192.168.122.252 (.254/.253 leased)", got)
	}
}

func TestAllocateIP_ExcludesBridgeInsideRange(t *testing.T) {
	// Bridge IP set to the top of the range — must be skipped even with no leases.
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: sampleNetXML, leases: leasesWithIPs()}.run)

	got, err := AllocateIP("default", "192.168.122.254")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "192.168.122.253" {
		t.Errorf("AllocateIP = %q, want 192.168.122.253 (bridge holds .254)", got)
	}
}

func TestAllocateIP_LowBoundaryInclusive(t *testing.T) {
	// Everything .3..254 used → only .2 (range start, inclusive) free.
	used := make([]int, 0, 252)
	for o := 3; o <= 254; o++ {
		used = append(used, o)
	}
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: sampleNetXML, leases: leasesWithIPs(used...)}.run)

	got, err := AllocateIP("default", "192.168.122.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "192.168.122.2" {
		t.Errorf("AllocateIP = %q, want 192.168.122.2 (range start is inclusive)", got)
	}
}

func TestAllocateIP_Exhausted(t *testing.T) {
	// Entire range .2..254 used → exhaustion, no spill below the start.
	used := make([]int, 0, 253)
	for o := 2; o <= 254; o++ {
		used = append(used, o)
	}
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: sampleNetXML, leases: leasesWithIPs(used...)}.run)

	_, err := AllocateIP("default", "192.168.122.1")
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if !strings.Contains(err.Error(), "no free IP") {
		t.Errorf("error = %q, want 'no free IP'", err.Error())
	}
}

func TestAllocateIP_NoRange(t *testing.T) {
	xml := `<network><name>default</name>
  <ip address='192.168.122.1' netmask='255.255.255.0'></ip>
</network>`
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: xml, leases: sampleLeases}.run)

	_, err := AllocateIP("default", "192.168.122.1")
	if err == nil {
		t.Fatal("expected error for missing DHCP range")
	}
	if !strings.Contains(err.Error(), "no IPv4 DHCP range") {
		t.Errorf("error = %q, want 'no IPv4 DHCP range'", err.Error())
	}
}

func TestAllocateIP_SkipsIPv6Block(t *testing.T) {
	// IPv6 <ip> appears first; allocation must use the IPv4 block's range.
	xml := `<network><name>default</name>
  <ip family='ipv6' address='2001:db8::1' prefix='64'>
    <dhcp><range start='2001:db8::100' end='2001:db8::200'/></dhcp>
  </ip>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp><range start='192.168.122.2' end='192.168.122.254'/></dhcp>
  </ip>
</network>`
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: xml, leases: leasesWithIPs()}.run)

	got, err := AllocateIP("default", "192.168.122.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "192.168.122.254" {
		t.Errorf("AllocateIP = %q, want IPv4 192.168.122.254 (IPv6 block must be skipped)", got)
	}
}

func TestAllocateIP_SkipsDHCPlessIPv4Block(t *testing.T) {
	// A routed secondary IPv4 subnet with no <dhcp> is listed before the real one.
	// ipv4DHCP must skip the empty block so the DHCP-bearing block drives
	// allocation; otherwise AllocateIP would fail with "no IPv4 DHCP range".
	xml := `<network><name>default</name>
  <ip address='192.168.200.1' netmask='255.255.255.0'></ip>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp><range start='192.168.122.2' end='192.168.122.254'/></dhcp>
  </ip>
</network>`
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: xml, leases: leasesWithIPs()}.run)

	got, err := AllocateIP("default", "192.168.122.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "192.168.122.254" {
		t.Errorf("AllocateIP = %q, want 192.168.122.254 (DHCP-less IPv4 block must be skipped)", got)
	}
}

func TestAllocateIP_DumpXMLError(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpErr: errors.New("virsh down")}.run)

	_, err := AllocateIP("default", "192.168.122.1")
	if err == nil || !strings.Contains(err.Error(), "net-dumpxml") {
		t.Errorf("error = %v, want net-dumpxml failure", err)
	}
}

func TestAllocateIP_LeasesError(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: sampleNetXML, leaseErr: errors.New("lease fail")}.run)

	_, err := AllocateIP("default", "192.168.122.1")
	if err == nil || !strings.Contains(err.Error(), "net-dhcp-leases") {
		t.Errorf("error = %v, want net-dhcp-leases failure", err)
	}
}

func TestAllocateIP_MalformedRange(t *testing.T) {
	xml := strings.Replace(sampleNetXML, "192.168.122.254", "not-an-ip", 1)
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: xml, leases: leasesWithIPs()}.run)

	_, err := AllocateIP("default", "192.168.122.1")
	if err == nil || !strings.Contains(err.Error(), "range end") {
		t.Errorf("error = %v, want range end parse failure", err)
	}
}

// ---------- parseLeasedIPs ----------

func TestParseLeasedIPs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"two real leases", sampleLeases, []string{"192.168.122.177", "192.168.122.5"}},
		{"empty", "", nil},
		{"header and separator only", leasesWithIPs(), nil},
		{"single lease", leasesWithIPs(42), []string{"192.168.122.42"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLeasedIPs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseLeasedIPs_IgnoresColonFields(t *testing.T) {
	// MAC and Client-ID columns contain colons but no "/" — must not be picked.
	ips := parseLeasedIPs(sampleLeases)
	for _, ip := range ips {
		if strings.Contains(ip, ":") {
			t.Errorf("parsed a non-IP field: %q", ip)
		}
	}
}

// ---------- parseNetworkActive ----------

func TestParseNetworkActive(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"active yes", "Name: default\nActive:         yes\nPersistent: yes", true},
		{"active no", "Name: default\nActive:         no", false},
		{"uppercase yes", "Active: YES", true},
		{"missing active line", "Name: default\nPersistent: yes", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseNetworkActive(tc.in); got != tc.want {
				t.Errorf("parseNetworkActive(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ---------- NetworkActive ----------

func TestNetworkActive_Active(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{netInfo: "Name: default\nActive:         yes"}.run)
	active, err := NetworkActive("default")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("expected active=true")
	}
}

func TestNetworkActive_Inactive(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{netInfo: "Name: default\nActive:         no"}.run)
	active, err := NetworkActive("default")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("expected active=false")
	}
}

func TestNetworkActive_Error(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{netInfoErr: errors.New("no such network")}.run)
	_, err := NetworkActive("default")
	if err == nil || !strings.Contains(err.Error(), "net-info") {
		t.Errorf("error = %v, want net-info failure", err)
	}
}

// ---------- GetMAC ----------

func TestGetMAC_Success(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{domiflist: sampleDomiflist}.run)
	mac, err := GetMAC("foo")
	if err != nil {
		t.Fatal(err)
	}
	if mac != "52:54:00:ab:cd:ef" {
		t.Errorf("GetMAC = %q, want 52:54:00:ab:cd:ef", mac)
	}
}

func TestGetMAC_NoMACInOutput(t *testing.T) {
	header := " Interface   Type      Source    Model    MAC\n----\n"
	mockRunnerWithFunc(t, fakeVirsh{domiflist: header}.run)
	_, err := GetMAC("foo")
	if err == nil || !strings.Contains(err.Error(), "no MAC found") {
		t.Errorf("error = %v, want 'no MAC found'", err)
	}
}

func TestGetMAC_Error(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{domiflistErr: errors.New("domain not found")}.run)
	_, err := GetMAC("foo")
	if err == nil || !strings.Contains(err.Error(), "domiflist") {
		t.Errorf("error = %v, want domiflist failure", err)
	}
}

// ---------- Add/Remove reservation ----------

func TestAddDHCPReservation_Args(t *testing.T) {
	recs := mockRunner(t)
	if err := AddDHCPReservation("default", "52:54:00:ab:cd:ef", "foo", "192.168.122.254"); err != nil {
		t.Fatal(err)
	}
	if len(*recs) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*recs))
	}
	rec := (*recs)[0]
	if rec.name != "virsh" {
		t.Errorf("command = %q, want virsh", rec.name)
	}
	args := strings.Join(rec.args, " ")
	for _, want := range []string{
		"--connect qemu:///system",
		"net-update default add ip-dhcp-host",
		"<host mac='52:54:00:ab:cd:ef' name='motoko-foo' ip='192.168.122.254'/>",
		"--live --config",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
}

func TestAddDHCPReservation_Error(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", errors.New("net-update boom")
	})
	err := AddDHCPReservation("default", "52:54:00:ab:cd:ef", "foo", "192.168.122.254")
	if err == nil || !strings.Contains(err.Error(), "net-update add ip-dhcp-host") {
		t.Errorf("error = %v, want add wrap", err)
	}
}

func TestRemoveDHCPReservation_Args(t *testing.T) {
	recs := mockRunner(t)
	if err := RemoveDHCPReservation("default", "52:54:00:ab:cd:ef", "foo", "192.168.122.254"); err != nil {
		t.Fatal(err)
	}
	args := strings.Join((*recs)[0].args, " ")
	for _, want := range []string{
		"net-update default delete ip-dhcp-host",
		"<host mac='52:54:00:ab:cd:ef' name='motoko-foo' ip='192.168.122.254'/>",
		"--live --config",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
}

func TestRemoveDHCPReservation_Error(t *testing.T) {
	mockRunnerWithFunc(t, func(string, ...string) (string, string, error) {
		return "", "", errors.New("boom")
	})
	err := RemoveDHCPReservation("default", "52:54:00:ab:cd:ef", "foo", "192.168.122.254")
	if err == nil || !strings.Contains(err.Error(), "net-update delete ip-dhcp-host") {
		t.Errorf("error = %v, want delete wrap", err)
	}
}

// ---------- FindReservationByName ----------

const netXMLWithHosts = `<network><name>default</name>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.122.2' end='192.168.122.254'/>
      <host mac='52:54:00:11:22:33' name='motoko-foo' ip='192.168.122.254'/>
      <host mac='52:54:00:44:55:66' name='motoko-bar' ip='192.168.122.253'/>
    </dhcp>
  </ip>
</network>`

func TestFindReservationByName_PicksMatching(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: netXMLWithHosts}.run)
	mac, ip, found := FindReservationByName("default", "bar")
	if !found {
		t.Fatal("expected to find motoko-bar")
	}
	if mac != "52:54:00:44:55:66" || ip != "192.168.122.253" {
		t.Errorf("got mac=%q ip=%q, want 52:54:00:44:55:66 / 192.168.122.253", mac, ip)
	}
}

func TestFindReservationByName_NotFound(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: netXMLWithHosts}.run)
	if _, _, found := FindReservationByName("default", "nope"); found {
		t.Error("expected found=false for unknown name")
	}
}

func TestFindReservationByName_NoHosts(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: sampleNetXML}.run)
	if _, _, found := FindReservationByName("default", "foo"); found {
		t.Error("expected found=false when no reservations exist")
	}
}

func TestFindReservationByName_SkipsDHCPlessIPv4Block(t *testing.T) {
	// The reservation lives in a later IPv4 block that carries hosts but no range.
	// ipv4DHCP must keep scanning past the leading empty block (exercising the
	// len(Hosts)>0 arm of its selection condition) to find it.
	xml := `<network><name>default</name>
  <ip address='192.168.200.1' netmask='255.255.255.0'></ip>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp><host mac='52:54:00:aa:bb:cc' name='motoko-foo' ip='192.168.122.254'/></dhcp>
  </ip>
</network>`
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: xml}.run)

	mac, ip, found := FindReservationByName("default", "foo")
	if !found {
		t.Fatal("expected to find motoko-foo in a later IPv4 block")
	}
	if mac != "52:54:00:aa:bb:cc" || ip != "192.168.122.254" {
		t.Errorf("got mac=%q ip=%q, want 52:54:00:aa:bb:cc / 192.168.122.254", mac, ip)
	}
}

func TestFindReservationByName_DumpError(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpErr: errors.New("boom")}.run)
	if _, _, found := FindReservationByName("default", "foo"); found {
		t.Error("expected found=false on net-dumpxml error")
	}
}

// ---------- hostXML ----------

func TestHostXML(t *testing.T) {
	got := hostXML("52:54:00:ab:cd:ef", "motoko-foo", "192.168.122.50")
	want := "<host mac='52:54:00:ab:cd:ef' name='motoko-foo' ip='192.168.122.50'/>"
	if got != want {
		t.Errorf("hostXML = %q, want %q", got, want)
	}
}

// ---------- dumpNetworkXML ----------

func TestDumpNetworkXML_ParseError(t *testing.T) {
	mockRunnerWithFunc(t, fakeVirsh{dumpXML: "<network><unclosed"}.run)
	_, err := dumpNetworkXML("default")
	if err == nil || !strings.Contains(err.Error(), "parsing network XML") {
		t.Errorf("error = %v, want parse failure", err)
	}
}
