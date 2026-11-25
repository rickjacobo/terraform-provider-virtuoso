package provider

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// AWS instances launched through virtuOSo. The server does the launching, so the AWS credentials stay on the
// virtuOSo server and never reach Terraform. Every call needs an admin API key.

const awsPath = "/api/v1/fleet/aws"

type AWSPort struct {
	Port   int64  `json:"port"`
	Source string `json:"source,omitempty"`
}

type AWSLaunchRequest struct {
	Name         string    `json:"name"`
	InstanceType string    `json:"instance_type,omitempty"`
	Region       string    `json:"region,omitempty"`
	OS           string    `json:"os,omitempty"`
	SSHKey       string    `json:"ssh_key,omitempty"`
	UserScript   string    `json:"user_script,omitempty"`
	DiskGB       int64     `json:"disk_gb,omitempty"`
	Ports        []AWSPort `json:"ports,omitempty"`
	// ConfirmCost is always true from Terraform: declaring the resource and running apply is the confirmation.
	ConfirmCost bool `json:"confirm_cost"`
	// Tailscale is only ever sent as false, to keep the instance off the tailnet.
	Tailscale *bool `json:"tailscale,omitempty"`
}

type AWSLaunchResponse struct {
	Name         string  `json:"name"`
	Password     string  `json:"password"`
	Status       string  `json:"status"`
	Region       string  `json:"region"`
	InstanceType string  `json:"instance_type"`
	HourlyUSD    float64 `json:"estimated_hourly_usd"`
}

type AWSInstance struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Region    string   `json:"region"`
	Type      string   `json:"type"`
	State     string   `json:"state"`
	PublicIP  string   `json:"public_ip"`
	PrivateIP string   `json:"private_ip"`
	VCPUs     int64    `json:"vcpus"`
	HourlyUSD *float64 `json:"estimated_hourly_usd"`
}

// Address is the public address, or the private one when there is no public address.
func (i AWSInstance) Address() string {
	if i.PublicIP != "" {
		return i.PublicIP
	}
	return i.PrivateIP
}

type awsList struct {
	Configured bool          `json:"configured"`
	Loaded     bool          `json:"loaded"`
	Error      string        `json:"error"`
	Instances  []AWSInstance `json:"instances"`
}

func (c *VirtuosoClient) LaunchAWSInstance(req AWSLaunchRequest) (*AWSLaunchResponse, error) {
	req.ConfirmCost = true
	var out AWSLaunchResponse
	if err := c.postAccepted(awsPath+"/instances", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAWSInstances reads the instances virtuOSo launched. It asks the server to read AWS now, so a new
// instance appears without waiting for the server's own once a minute refresh.
func (c *VirtuosoClient) ListAWSInstances() (*awsList, error) {
	var out awsList
	if _, err := c.getJSON(awsPath+"?refresh=1", &out); err != nil {
		return nil, err
	}
	if !out.Configured {
		return nil, fmt.Errorf("AWS is not set up on the virtuOSo server: add credentials under Settings > AWS")
	}
	if out.Error != "" {
		return nil, fmt.Errorf("virtuOSo could not read AWS: %s", out.Error)
	}
	return &out, nil
}

// GetAWSInstance returns the instance with this id, or nil if there is no such instance any more.
func (c *VirtuosoClient) GetAWSInstance(id string) (*AWSInstance, error) {
	list, err := c.ListAWSInstances()
	if err != nil {
		return nil, err
	}
	for i := range list.Instances {
		if list.Instances[i].ID == id {
			return &list.Instances[i], nil
		}
	}
	return nil, nil
}

// FindAWSInstance looks an instance up by name, and by region when one is given.
func (c *VirtuosoClient) FindAWSInstance(name, region string) (*AWSInstance, error) {
	list, err := c.ListAWSInstances()
	if err != nil {
		return nil, err
	}
	for i := range list.Instances {
		in := list.Instances[i]
		if in.Name == name && (region == "" || in.Region == region) {
			return &in, nil
		}
	}
	return nil, nil
}

// WaitForAWSInstance waits until the instance is running and has an address.
func (c *VirtuosoClient) WaitForAWSInstance(name, region string, timeout time.Duration) (*AWSInstance, error) {
	deadline := time.Now().Add(timeout)
	for {
		in, err := c.FindAWSInstance(name, region)
		if err != nil {
			return nil, err
		}
		if in != nil && in.State == "running" && in.Address() != "" {
			return in, nil
		}
		if time.Now().After(deadline) {
			state := "not visible in AWS yet"
			if in != nil {
				state = "state " + in.State
			}
			return nil, fmt.Errorf("instance %q was not running after %s (%s)", name, timeout, state)
		}
		time.Sleep(appPollInterval)
	}
}

// TerminateAWSInstance terminates an instance launched through virtuOSo and removes its security groups. An
// instance that is already gone counts as terminated.
func (c *VirtuosoClient) TerminateAWSInstance(id string) error {
	if !strings.HasPrefix(id, "i-") {
		return fmt.Errorf("%q is not an instance id", id)
	}
	return c.deleteIgnoringMissing(awsPath + "/instances/" + url.PathEscape(id))
}
