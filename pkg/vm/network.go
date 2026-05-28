package vm

import (
	"encoding/xml"
	"fmt"
	"net/netip"
	"strings"
)

// netXML is a partial unmarshal target for `virsh net-dumpxml`. Only the fields
// needed for IP allocation and reservation lookup are modeled.
type netXML struct {
	XMLName xml.Name  `xml:"network"`
	IPs     []ipBlock `xml:"ip"`
}

type ipBlock struct {
	Address string    `xml:"address,attr"`
	DHCP    dhcpBlock `xml:"dhcp"`
}

type dhcpBlock struct {
	Range dhcpRange  `xml:"range"`
	Hosts []dhcpHost `xml:"host"`
}

type dhcpRange struct {
	Start string `xml:"start,attr"`
	End   string `xml:"end,attr"`
}

type dhcpHost struct {
	MAC  string `xml:"mac,attr"`
	Name string `xml:"name,attr"`
	IP   string `xml:"ip,attr"`
}

// ipv4DHCP returns the DHCP block of the first IPv4 <ip> element that actually
// carries DHCP config (a range or host reservations), or nil. IPv6 blocks are
// skipped by requiring a dot in the address; IPv4 blocks with no <dhcp> (e.g. a
// routed secondary subnet listed first) are skipped so a later DHCP-bearing
// block still wins instead of being masked by an empty one.
func (n *netXML) ipv4DHCP() *dhcpBlock {
	for i := range n.IPs {
		if !strings.Contains(n.IPs[i].Address, ".") {
			continue
		}
		d := &n.IPs[i].DHCP
		if d.Range.Start != "" || len(d.Hosts) > 0 {
			return d
		}
	}
	return nil
}

// GetMAC reads the MAC address libvirt assigned to the instance's NIC.
// The MAC is fixed at define time and persisted in domain XML, so this is
// readable while the VM is still powered off (before Start).
func GetMAC(name string) (string, error) {
	stdout, _, err := runCmd("virsh", libvirtArgs("domiflist", libvirtName(name))...)
	if err != nil {
		return "", fmt.Errorf("domiflist: %w", err)
	}
	mac := parseMACFromDomiflist(stdout)
	if mac == "" {
		return "", fmt.Errorf("no MAC found for instance %q", name)
	}
	return mac, nil
}

// NetworkActive reports whether the libvirt network is running. A reservation
// added with --live requires an active network; without this check a --config
// half-write could leave a phantom reservation that only applies on net restart.
func NetworkActive(network string) (bool, error) {
	stdout, _, err := runCmd("virsh", libvirtArgs("net-info", network)...)
	if err != nil {
		return false, fmt.Errorf("net-info: %w", err)
	}
	return parseNetworkActive(stdout), nil
}

// parseNetworkActive extracts the "Active: yes|no" line from `virsh net-info`.
func parseNetworkActive(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Active:" {
			return strings.EqualFold(fields[1], "yes")
		}
	}
	return false
}

// AllocateIP picks a free IPv4 address from the network's DHCP range.
//
// It scans the range high-end-down, returning the first address not already
// taken by a static <host> reservation, a live DHCP lease, or the bridge.
// High-end allocation keeps motoko's static reservations clustered away from
// dnsmasq's low/hashed dynamic handouts, minimizing overlap with non-motoko VMs.
func AllocateIP(network, bridgeIP string) (string, error) {
	n, err := dumpNetworkXML(network)
	if err != nil {
		return "", err
	}
	dhcp := n.ipv4DHCP()
	if dhcp == nil || dhcp.Range.Start == "" || dhcp.Range.End == "" {
		return "", fmt.Errorf("network %q has no IPv4 DHCP range", network)
	}

	used := map[netip.Addr]bool{}
	addUsed := func(ip string) {
		if a, err := netip.ParseAddr(ip); err == nil {
			used[a] = true
		}
	}
	addUsed(bridgeIP)
	// Exclude every gateway address libvirt itself owns (the <ip address> of each
	// block), not just the configured bridge_ip. They normally match, but a
	// drifted config must never let us hand a VM the real gateway address.
	for i := range n.IPs {
		if strings.Contains(n.IPs[i].Address, ".") {
			addUsed(n.IPs[i].Address)
		}
	}
	for _, h := range dhcp.Hosts {
		addUsed(h.IP)
	}
	leases, err := leasedIPs(network)
	if err != nil {
		return "", err
	}
	for _, ip := range leases {
		addUsed(ip)
	}

	start, err := netip.ParseAddr(dhcp.Range.Start)
	if err != nil {
		return "", fmt.Errorf("parsing range start %q: %w", dhcp.Range.Start, err)
	}
	end, err := netip.ParseAddr(dhcp.Range.End)
	if err != nil {
		return "", fmt.Errorf("parsing range end %q: %w", dhcp.Range.End, err)
	}

	for a := end; a.Compare(start) >= 0; a = a.Prev() {
		if !used[a] {
			return a.String(), nil
		}
	}
	return "", fmt.Errorf("no free IP available in range %s-%s", dhcp.Range.Start, dhcp.Range.End)
}

// AddDHCPReservation pins ip to mac in the libvirt network. --live applies it to
// the running dnsmasq (so a VM booting moments later gets the reservation),
// --config persists it across host/network restarts.
func AddDHCPReservation(network, mac, name, ip string) error {
	return netUpdateHost(network, "add", mac, name, ip)
}

// RemoveDHCPReservation deletes the static reservation. libvirt matches the
// <host> entry by MAC, so a drifted IP/name still removes the right entry.
func RemoveDHCPReservation(network, mac, name, ip string) error {
	return netUpdateHost(network, "delete", mac, name, ip)
}

// netUpdateHost runs `virsh net-update <network> <op> ip-dhcp-host <host>` with
// --live --config. --live applies to the running dnsmasq and requires the
// network to be active (callers adding a reservation gate on NetworkActive);
// --config persists the change across restarts.
func netUpdateHost(network, op, mac, name, ip string) error {
	host := hostXML(mac, libvirtName(name), ip)
	if _, _, err := runCmd("virsh", libvirtArgs("net-update", network, op, "ip-dhcp-host", host, "--live", "--config")...); err != nil {
		return fmt.Errorf("net-update %s ip-dhcp-host: %w", op, err)
	}
	return nil
}

// FindReservationByName recovers a reservation's mac+ip from the network XML by
// its deterministic name (motoko-<name>). Used to clean up orphaned instances
// that have no state file. Returns found=false on any error or no match.
func FindReservationByName(network, name string) (mac, ip string, found bool) {
	n, err := dumpNetworkXML(network)
	if err != nil {
		return "", "", false
	}
	dhcp := n.ipv4DHCP()
	if dhcp == nil {
		return "", "", false
	}
	want := libvirtName(name)
	for _, h := range dhcp.Hosts {
		if h.Name == want {
			return h.MAC, h.IP, true
		}
	}
	return "", "", false
}

func hostXML(mac, name, ip string) string {
	return fmt.Sprintf("<host mac='%s' name='%s' ip='%s'/>", mac, name, ip)
}

func dumpNetworkXML(network string) (*netXML, error) {
	stdout, _, err := runCmd("virsh", libvirtArgs("net-dumpxml", network)...)
	if err != nil {
		return nil, fmt.Errorf("net-dumpxml: %w", err)
	}
	var n netXML
	if err := xml.Unmarshal([]byte(stdout), &n); err != nil {
		return nil, fmt.Errorf("parsing network XML: %w", err)
	}
	return &n, nil
}

func leasedIPs(network string) ([]string, error) {
	stdout, _, err := runCmd("virsh", libvirtArgs("net-dhcp-leases", network)...)
	if err != nil {
		return nil, fmt.Errorf("net-dhcp-leases: %w", err)
	}
	return parseLeasedIPs(stdout), nil
}

// parseLeasedIPs extracts lease IPs from `virsh net-dhcp-leases` output. It finds
// the CIDR-formatted field (contains both "/" and ".") rather than relying on
// column position, since the "MAC address"/"Client ID or DUID" headers split into
// multiple tokens under strings.Fields.
func parseLeasedIPs(output string) []string {
	var ips []string
	for _, line := range strings.Split(output, "\n") {
		for _, f := range strings.Fields(line) {
			if strings.Contains(f, "/") && strings.Contains(f, ".") {
				ips = append(ips, strings.SplitN(f, "/", 2)[0])
			}
		}
	}
	return ips
}
