package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// appPollInterval is how often the Wait* helpers re-check. Tests shorten it.
var appPollInterval = 10 * time.Second

// Kubernetes cluster API

type K8sWorkerResponse struct {
	Name        string `json:"name"`
	IP          string `json:"ip"`
	TailscaleIP string `json:"tailscale_ip"`
	Status      string `json:"status"`
}

type K8sClusterResponse struct {
	Name        string              `json:"name"`
	VMName      string              `json:"vm_name"`
	Status      string              `json:"status"`
	IP          string              `json:"ip"`
	TailscaleIP string              `json:"tailscale_ip"`
	Size        string              `json:"size"`
	DiskGB      int64               `json:"disk_gb"`
	ArgoCD      bool                `json:"argocd"`
	Headlamp    bool                `json:"headlamp"`
	K9s         bool                `json:"k9s"`
	Workers     []K8sWorkerResponse `json:"workers"`
}

type K8sCreateRequest struct {
	Name        string `json:"name"`
	Size        string `json:"size,omitempty"`
	DiskGB      int64  `json:"disk_gb,omitempty"`
	Bridged     bool   `json:"bridged,omitempty"`
	ArgoCD      bool   `json:"argocd,omitempty"`
	Headlamp    bool   `json:"headlamp,omitempty"`
	K9s         bool   `json:"k9s,omitempty"`
	WorkerCount int64  `json:"worker_count,omitempty"`
}

type K8sCredentials struct {
	ArgoCDPassword string `json:"argocd_password"`
	HeadlampToken  string `json:"headlamp_token"`
}

const k8sPath = "/api/v1/apps/k8s/clusters"

// getJSON GETs path and decodes the response. found is false on 404.
func (c *VirtuosoClient) getJSON(path string, out interface{}) (found bool, err error) {
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, c.parseError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("decoding response: %w", err)
	}
	return true, nil
}

// postAccepted sends a POST that the API answers with 202 Accepted.
func (c *VirtuosoClient) postAccepted(path string, body, out interface{}) error {
	resp, err := c.doRequest("POST", path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return c.parseError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// deleteIgnoringMissing deletes path; a 404 counts as success.
func (c *VirtuosoClient) deleteIgnoringMissing(path string) error {
	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) CreateK8sCluster(req K8sCreateRequest) (*K8sClusterResponse, error) {
	var out K8sClusterResponse
	if err := c.postAccepted(k8sPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) GetK8sCluster(name string) (*K8sClusterResponse, error) {
	var out K8sClusterResponse
	found, err := c.getJSON(k8sPath+"/"+url.PathEscape(name), &out)
	if err != nil || !found {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) DeleteK8sCluster(name string) error {
	return c.deleteIgnoringMissing(k8sPath + "/" + url.PathEscape(name))
}

func (c *VirtuosoClient) ScaleK8sCluster(name string, workers int64) error {
	return c.postAccepted(k8sPath+"/"+url.PathEscape(name)+"/scale", map[string]int64{"worker_count": workers}, nil)
}

func (c *VirtuosoClient) InstallK8sAddon(name, addon string) error {
	return c.postAccepted(k8sPath+"/"+url.PathEscape(name)+"/addons", map[string]string{"addon": addon}, nil)
}

func (c *VirtuosoClient) GetK8sCredentials(name string) (*K8sCredentials, error) {
	var out K8sCredentials
	found, err := c.getJSON(k8sPath+"/"+url.PathEscape(name)+"/credentials", &out)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("cluster %q not found", name)
	}
	return &out, nil
}

// GetK8sKubeconfig downloads the cluster's kubeconfig (YAML).
func (c *VirtuosoClient) GetK8sKubeconfig(name string) (string, error) {
	resp, err := c.doRequest("GET", k8sPath+"/"+url.PathEscape(name)+"/kubeconfig", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

// WaitForK8sCluster waits until the server VM is running and exactly `workers`
// worker VMs are up (status ready). A worker joins the cluster a little later.
func (c *VirtuosoClient) WaitForK8sCluster(name string, workers int64, timeout time.Duration) (*K8sClusterResponse, error) {
	deadline := time.Now().Add(timeout)
	for {
		cl, err := c.GetK8sCluster(name)
		if err != nil {
			return nil, err
		}
		if cl != nil && cl.Status == "running" && int64(len(cl.Workers)) == workers {
			ready := true
			for _, w := range cl.Workers {
				if w.Status != "ready" {
					ready = false
				}
			}
			if ready {
				return cl, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for cluster %q to be running with %d ready workers", name, workers)
		}
		time.Sleep(appPollInterval)
	}
}

// WaitForK8sKubeconfig retries until the kubeconfig can be read (the cluster API
// answers a little after the VM reports running).
func (c *VirtuosoClient) WaitForK8sKubeconfig(name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		kc, err := c.GetK8sKubeconfig(name)
		if err == nil && kc != "" {
			return kc, nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("empty kubeconfig")
			}
			return "", err
		}
		time.Sleep(appPollInterval)
	}
}

// Docker instance API

type AppInstanceResponse struct {
	Name        string `json:"name"`
	VMName      string `json:"vm_name"`
	Status      string `json:"status"`
	IP          string `json:"ip"`
	TailscaleIP string `json:"tailscale_ip"`
	Size        string `json:"size"`
	DiskGB      int64  `json:"disk_gb"`
}

type DockerCreateRequest struct {
	Name    string `json:"name"`
	Size    string `json:"size,omitempty"`
	DiskGB  int64  `json:"disk_gb,omitempty"`
	Bridged bool   `json:"bridged,omitempty"`
}

const dockerPath = "/api/v1/apps/docker/instances"

func (c *VirtuosoClient) CreateDockerInstance(req DockerCreateRequest) (*AppInstanceResponse, error) {
	var out AppInstanceResponse
	if err := c.postAccepted(dockerPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) GetDockerInstance(name string) (*AppInstanceResponse, error) {
	var out AppInstanceResponse
	found, err := c.getJSON(dockerPath+"/"+url.PathEscape(name), &out)
	if err != nil || !found {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) DeleteDockerInstance(name string) error {
	return c.deleteIgnoringMissing(dockerPath + "/" + url.PathEscape(name))
}

func (c *VirtuosoClient) WaitForDockerInstance(name string, timeout time.Duration) (*AppInstanceResponse, error) {
	deadline := time.Now().Add(timeout)
	for {
		inst, err := c.GetDockerInstance(name)
		if err != nil {
			return nil, err
		}
		if inst != nil && inst.Status == "running" {
			return inst, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for Docker instance %q to be running", name)
		}
		time.Sleep(appPollInterval)
	}
}
