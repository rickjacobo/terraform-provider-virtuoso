package provider

import (
	"net/url"
	"strings"
	"time"
)

// Fleet calls. A virtuOSo server that is the hub of a fleet can start, list and delete VMs on its nodes.

// FleetLaunchRequest is LaunchRequest with the node to run on.
type FleetLaunchRequest struct {
	LaunchRequest
	Node string `json:"node"`
}

type fleetLaunchResponse struct {
	Node   string `json:"node"`
	Status int    `json:"status"`
	Result struct {
		Password string `json:"password"`
	} `json:"result"`
}

// longer returns a client that waits longer for an answer. A fleet launch can wait for a node to apply a
// VPC, and a delete that stops the VM first waits for it to shut off.
func (c *VirtuosoClient) longer(d time.Duration) *VirtuosoClient {
	hc := *c.httpClient
	hc.Timeout = d
	cp := *c
	cp.httpClient = &hc
	return &cp
}

// FleetLaunch starts a VM on the named node, through the hub. It returns the password the node generated.
func (c *VirtuosoClient) FleetLaunch(req FleetLaunchRequest) (string, error) {
	var out fleetLaunchResponse
	if _, err := c.longer(3*time.Minute).send("POST", "/api/v1/fleet/vms", req, &out, 200, 201, 202); err != nil {
		return "", err
	}
	return out.Result.Password, nil
}

// DeleteFleetVM deletes a VM on a node, with its disk. With stop, a running VM is shut down first. It reports
// false when the node or VM is not there.
func (c *VirtuosoClient) DeleteFleetVM(node, name string, stop bool) (bool, error) {
	p := "/api/v1/fleet/vms/" + url.PathEscape(node) + "/" + url.PathEscape(name) + "?confirm=true"
	if stop {
		p += "&stop=true"
	}
	return c.longer(5*time.Minute).send("DELETE", p, nil, nil, 200)
}

// vpcRetryWait is the pause between launches that were told the VPC is not there yet.
var vpcRetryWait = 5 * time.Second

// retryVPCNotFound runs a launch again for a short while when the server says the VPC is not there yet. A VPC
// made a moment ago can take a few seconds to reach a server.
func retryVPCNotFound(attempts int, wait time.Duration, do func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = do(); err == nil || !strings.Contains(err.Error(), "VPC not found") {
			return err
		}
		time.Sleep(wait)
	}
	return err
}

// FleetNodesOf lists the fleet nodes whose VM list has a VM of this name. A VM moved between servers outside
// Terraform is on a different node than the one the state remembers.
func (c *VirtuosoClient) FleetNodesOf(name string) ([]string, error) {
	var out struct {
		VMs []struct {
			Node     string `json:"node"`
			Name     string `json:"name"`
			Provider string `json:"provider"`
		} `json:"vms"`
	}
	if _, err := c.send("GET", "/api/v1/fleet/vms", nil, &out, 200); err != nil {
		return nil, err
	}
	var nodes []string
	for _, v := range out.VMs {
		if v.Name == name && v.Node != "" && (v.Provider == "" || v.Provider == "On-Prem") {
			nodes = append(nodes, v.Node)
		}
	}
	return nodes, nil
}
