package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// VPC types, as the virtuOSo REST API sends them.

type VPCCard struct {
	VM     string `json:"vm"`
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
	Server string `json:"server"`
}

type VPCResponse struct {
	Name      string    `json:"name"`
	CIDR      string    `json:"cidr"`
	MTU       int64     `json:"mtu"`
	Attached  int64     `json:"attached"`
	Capacity  int64     `json:"capacity"`
	CreatedAt string    `json:"created_at"`
	VMs       []VPCCard `json:"vms"`
}

type SecurityGroupRule struct {
	ID          int64  `json:"id"`
	Direction   string `json:"direction"`
	Protocol    string `json:"protocol"`
	PortFrom    int64  `json:"port_from"`
	PortTo      int64  `json:"port_to"`
	CIDR        string `json:"cidr,omitempty"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
}

type SecurityGroupResponse struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Rules       []SecurityGroupRule `json:"rules"`
	VMs         []string            `json:"vms"`
}

type FloatingIPResponse struct {
	Address string `json:"address"`
	VPC     string `json:"vpc,omitempty"`
	VM      string `json:"vm,omitempty"`
}

// send runs one request and decodes a JSON answer into out when the status is one of ok. A 404 on a GET or
// DELETE gives (false, nil) so a caller can tell "gone" from a failure; on anything else it is an error.
func (c *VirtuosoClient) send(method, path string, body, out interface{}, ok ...int) (bool, error) {
	resp, err := c.doRequest(method, path, body)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && (method == "GET" || method == "DELETE") {
		return false, nil
	}
	good := false
	for _, s := range ok {
		if resp.StatusCode == s {
			good = true
		}
	}
	if !good {
		return false, c.parseError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return false, fmt.Errorf("decoding response: %w", err)
		}
	}
	return true, nil
}

func vpcPath(name string) string { return "/api/v1/vpcs/" + url.PathEscape(name) }

func groupPath(vpc, group string) string {
	return vpcPath(vpc) + "/security-groups/" + url.PathEscape(group)
}

// VPCs

func (c *VirtuosoClient) CreateVPC(name, cidr string, mtu int64) (*VPCResponse, error) {
	body := map[string]any{"name": name, "cidr": cidr}
	if mtu > 0 {
		body["mtu"] = mtu
	}
	var out VPCResponse
	if _, err := c.send("POST", "/api/v1/vpcs", body, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetVPC returns nil when there is no such VPC.
func (c *VirtuosoClient) GetVPC(name string) (*VPCResponse, error) {
	var out VPCResponse
	found, err := c.send("GET", vpcPath(name), nil, &out, http.StatusOK)
	if err != nil || !found {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) DeleteVPC(name string) error {
	_, err := c.send("DELETE", vpcPath(name), nil, nil, http.StatusOK)
	return err
}

// Security groups

func (c *VirtuosoClient) CreateSecurityGroup(vpc, name, description string) error {
	body := map[string]any{"name": name}
	if description != "" {
		body["description"] = description
	}
	_, err := c.send("POST", vpcPath(vpc)+"/security-groups", body, nil, http.StatusCreated)
	return err
}

// GetSecurityGroup returns nil when the VPC or the group is gone.
func (c *VirtuosoClient) GetSecurityGroup(vpc, name string) (*SecurityGroupResponse, error) {
	var out SecurityGroupResponse
	found, err := c.send("GET", groupPath(vpc, name), nil, &out, http.StatusOK)
	if err != nil || !found {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) DeleteSecurityGroup(vpc, name string) error {
	_, err := c.send("DELETE", groupPath(vpc, name), nil, nil, http.StatusOK)
	return err
}

func (c *VirtuosoClient) AddSecurityGroupRule(vpc, group string, r SecurityGroupRule) (*SecurityGroupRule, error) {
	body := map[string]any{"direction": r.Direction, "protocol": r.Protocol}
	if r.PortFrom != 0 {
		body["port_from"] = r.PortFrom
	}
	if r.PortTo != 0 {
		body["port_to"] = r.PortTo
	}
	if r.CIDR != "" {
		body["cidr"] = r.CIDR
	}
	if r.Group != "" {
		body["group"] = r.Group
	}
	if r.Description != "" {
		body["description"] = r.Description
	}
	var out SecurityGroupRule
	if _, err := c.send("POST", groupPath(vpc, group)+"/rules", body, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) RemoveSecurityGroupRule(vpc, group string, id int64) error {
	_, err := c.send("DELETE", fmt.Sprintf("%s/rules/%d", groupPath(vpc, group), id), nil, nil, http.StatusOK)
	return err
}

// VMSecurityGroups returns the groups a VM's card is in, or nil when the VM is not in the VPC.
func (c *VirtuosoClient) VMSecurityGroups(vpc, vm string) ([]string, bool, error) {
	var out struct {
		Groups []string `json:"security_groups"`
	}
	found, err := c.send("GET", vpcPath(vpc)+"/vms/"+url.PathEscape(vm)+"/security-groups", nil, &out, http.StatusOK)
	return out.Groups, found, err
}

func (c *VirtuosoClient) SetVMSecurityGroups(vpc, vm string, groups []string) ([]string, error) {
	if groups == nil {
		groups = []string{}
	}
	var out struct {
		Groups []string `json:"security_groups"`
	}
	_, err := c.send("PUT", vpcPath(vpc)+"/vms/"+url.PathEscape(vm)+"/security-groups",
		map[string]any{"security_groups": groups}, &out, http.StatusOK)
	return out.Groups, err
}

// Floating IPs

// AllocateFloatingIP takes an address from the pool; with address set it takes that one.
func (c *VirtuosoClient) AllocateFloatingIP(address string) (*FloatingIPResponse, error) {
	var body interface{}
	if address != "" {
		body = map[string]any{"address": address}
	}
	var out FloatingIPResponse
	if _, err := c.send("POST", "/api/v1/vpc/floating-ips", body, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetFloatingIP returns nil when the address is not allocated.
func (c *VirtuosoClient) GetFloatingIP(address string) (*FloatingIPResponse, error) {
	var out struct {
		IPs []FloatingIPResponse `json:"floating_ips"`
	}
	if _, err := c.send("GET", "/api/v1/vpc/floating-ips", nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	for i := range out.IPs {
		if out.IPs[i].Address == address {
			return &out.IPs[i], nil
		}
	}
	return nil, nil
}

func (c *VirtuosoClient) AttachFloatingIP(address, vpc, vm string) error {
	_, err := c.send("POST", "/api/v1/vpc/floating-ips/"+url.PathEscape(address)+"/attach",
		map[string]any{"vpc": vpc, "vm": vm}, nil, http.StatusOK)
	return err
}

// DetachFloatingIP detaches an address. An address the server does not know is already detached, so it is
// not an error: a VM replace or a retried destroy can leave nothing to detach.
func (c *VirtuosoClient) DetachFloatingIP(address string) error {
	resp, err := c.doRequest("POST", "/api/v1/vpc/floating-ips/"+url.PathEscape(address)+"/detach", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return c.parseError(resp)
}

func (c *VirtuosoClient) ReleaseFloatingIP(address string) error {
	// The server answers 204 with no body, which is success.
	_, err := c.send("DELETE", "/api/v1/vpc/floating-ips/"+url.PathEscape(address), nil, nil, http.StatusOK, http.StatusNoContent)
	return err
}

// ListVPCs returns every VPC the key can see.
func (c *VirtuosoClient) ListVPCs() ([]VPCResponse, error) {
	var out struct {
		VPCs []VPCResponse `json:"vpcs"`
	}
	if _, err := c.send("GET", "/api/v1/vpcs", nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return out.VPCs, nil
}

// FindVPCOfVM returns the name of the VPC a VM is in, or "" when it is in none.
func (c *VirtuosoClient) FindVPCOfVM(vm string) (string, error) {
	list, err := c.ListVPCs()
	if err != nil {
		return "", err
	}
	for _, v := range list {
		full, err := c.GetVPC(v.Name)
		if err != nil {
			return "", err
		}
		if full == nil {
			continue
		}
		for _, card := range full.VMs {
			if card.VM == vm {
				return v.Name, nil
			}
		}
	}
	return "", nil
}

// Peerings

// PeeringResponse is one peering. The API sorts the pair by name, so VPCA is the name that sorts first, not
// necessarily the one a config lists first.
type PeeringResponse struct {
	VPCA      string `json:"vpc_a"`
	VPCB      string `json:"vpc_b"`
	CIDRA     string `json:"cidr_a"`
	CIDRB     string `json:"cidr_b"`
	CreatedAt string `json:"created_at"`
}

func peeringPath(a, b string) string {
	return "/api/v1/vpc/peerings/" + url.PathEscape(a) + "/" + url.PathEscape(b)
}

func (c *VirtuosoClient) CreatePeering(a, b string) (*PeeringResponse, error) {
	var out PeeringResponse
	if _, err := c.send("POST", "/api/v1/vpc/peerings", map[string]any{"vpc_a": a, "vpc_b": b}, &out, http.StatusCreated); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPeering returns nil when the two VPCs are not peered. The names may be given in either order.
func (c *VirtuosoClient) GetPeering(a, b string) (*PeeringResponse, error) {
	var out PeeringResponse
	found, err := c.send("GET", peeringPath(a, b), nil, &out, http.StatusOK)
	if err != nil || !found {
		return nil, err
	}
	return &out, nil
}

// DeletePeering ends a peering. One that is already gone is not an error.
func (c *VirtuosoClient) DeletePeering(a, b string) error {
	_, err := c.send("DELETE", peeringPath(a, b), nil, nil, http.StatusOK)
	return err
}
