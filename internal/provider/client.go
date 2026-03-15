package provider

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type VirtuosoClient struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

func NewVirtuosoClient(endpoint, apiKey string, insecure bool) *VirtuosoClient {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &VirtuosoClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

// API response types

type VMResponse struct {
	Name          string  `json:"name"`
	State         string  `json:"state"`
	IP            string  `json:"ip"`
	VCPUs         int64   `json:"vcpus"`
	MemoryMB      int64   `json:"memory_mb"`
	Autostart     string  `json:"autostart"`
	HasVNC        bool    `json:"has_vnc"`
	DiskCapGB     float64 `json:"disk_cap_gb"`
	DiskUsedGB    float64 `json:"disk_used_gb"`
	CloudInit     string  `json:"cloud_init"`
	Network       string  `json:"network"`
	Protected     bool    `json:"protected"`
	Password      string  `json:"password,omitempty"`
	OSID          string  `json:"os_id,omitempty"`
	StatusMessage string  `json:"status_message,omitempty"`
}

type LaunchRequest struct {
	Name       string `json:"name"`
	Size       string `json:"size,omitempty"`
	OS         string `json:"os"`
	SSHKey     string `json:"ssh_key,omitempty"`
	DiskGB     int64  `json:"disk_gb,omitempty"`
	Password   string `json:"password,omitempty"`
	VNC        bool   `json:"vnc,omitempty"`
	Desktop    bool   `json:"desktop,omitempty"`
	UserScript string `json:"user_script,omitempty"`
	ISO        string `json:"iso,omitempty"`
	Bridged    bool   `json:"bridged,omitempty"`
}

type LaunchResponse struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Status   string `json:"status"`
}

type SSHKeyResponse struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	CreatedAt string `json:"created_at"`
}

type SSHKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

type VMAccessEntry struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type APIError struct {
	Error  string `json:"error"`
	Status int    `json:"status"`
}

// HTTP helpers

func (c *VirtuosoClient) doRequest(method, path string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.endpoint+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

func (c *VirtuosoClient) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var apiErr APIError
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, apiErr.Error)
	}
	return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
}

// VM operations

func (c *VirtuosoClient) ListVMs() ([]VMResponse, error) {
	resp, err := c.doRequest("GET", "/api/v1/vms", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var vms []VMResponse
	if err := json.NewDecoder(resp.Body).Decode(&vms); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return vms, nil
}

func (c *VirtuosoClient) GetVM(name string) (*VMResponse, error) {
	resp, err := c.doRequest("GET", "/api/v1/vms/"+name, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var vm VMResponse
	if err := json.NewDecoder(resp.Body).Decode(&vm); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &vm, nil
}

func (c *VirtuosoClient) LaunchVM(req LaunchRequest) (*LaunchResponse, error) {
	resp, err := c.doRequest("POST", "/api/v1/vms", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return nil, c.parseError(resp)
	}

	var result LaunchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &result, nil
}

func (c *VirtuosoClient) DeleteVM(name string) error {
	resp, err := c.doRequest("DELETE", "/api/v1/vms/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // already gone
	}
	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) StartVM(name string) error {
	resp, err := c.doRequest("POST", "/api/v1/vms/"+name+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) StopVM(name string) error {
	resp, err := c.doRequest("POST", "/api/v1/vms/"+name+"/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) KillVM(name string) error {
	resp, err := c.doRequest("POST", "/api/v1/vms/"+name+"/kill", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) RestartVM(name string) error {
	resp, err := c.doRequest("POST", "/api/v1/vms/"+name+"/restart", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) ResizeVM(name string, sizeGB int64) error {
	body := map[string]int64{"size_gb": sizeGB}
	resp, err := c.doRequest("POST", "/api/v1/vms/"+name+"/resize", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) ChangeNetwork(name, network string) error {
	body := map[string]string{"network": network}
	resp, err := c.doRequest("PUT", "/api/v1/vms/"+name+"/network", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) SetAutostart(name string, enabled bool) error {
	body := map[string]bool{"enabled": enabled}
	resp, err := c.doRequest("PUT", "/api/v1/vms/"+name+"/autostart", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) SetProtected(name string, protected bool) error {
	body := map[string]bool{"protected": protected}
	resp, err := c.doRequest("PUT", "/api/v1/vms/"+name+"/protected", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) WaitForVM(name string, timeout time.Duration) (*VMResponse, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		vm, err := c.GetVM(name)
		if err != nil {
			return nil, err
		}
		if vm == nil {
			// 404 — still being created (image download phase)
			time.Sleep(10 * time.Second)
			continue
		}
		if vm.State == "running" || vm.State == "shutoff" {
			return vm, nil
		}
		time.Sleep(10 * time.Second)
	}
	return nil, fmt.Errorf("timeout waiting for VM %q to be ready", name)
}

func (c *VirtuosoClient) WaitForVMIP(name string, timeout time.Duration) (*VMResponse, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		vm, err := c.GetVM(name)
		if err != nil {
			return nil, err
		}
		if vm != nil && vm.IP != "" {
			return vm, nil
		}
		time.Sleep(5 * time.Second)
	}
	return nil, fmt.Errorf("timeout waiting for VM %q to get an IP address", name)
}

// SSH key operations

func (c *VirtuosoClient) ListSSHKeys() ([]SSHKeyResponse, error) {
	resp, err := c.doRequest("GET", "/api/v1/sshkeys", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var keys []SSHKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return keys, nil
}

func (c *VirtuosoClient) CreateSSHKey(req SSHKeyRequest) (*SSHKeyResponse, error) {
	resp, err := c.doRequest("POST", "/api/v1/sshkeys", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, c.parseError(resp)
	}

	var key SSHKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&key); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &key, nil
}

func (c *VirtuosoClient) DeleteSSHKey(id int64) error {
	resp, err := c.doRequest("DELETE", fmt.Sprintf("/api/v1/sshkeys/%d", id), nil)
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

// VM access operations

func (c *VirtuosoClient) ListVMAccess(vmName string) ([]VMAccessEntry, error) {
	resp, err := c.doRequest("GET", "/api/v1/vms/"+vmName+"/access", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var entries []VMAccessEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return entries, nil
}

func (c *VirtuosoClient) GrantVMAccess(vmName string, userID int64) error {
	body := map[string]int64{"user_id": userID}
	resp, err := c.doRequest("POST", "/api/v1/vms/"+vmName+"/access", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	return nil
}

func (c *VirtuosoClient) RevokeVMAccess(vmName string, userID int64) error {
	resp, err := c.doRequest("DELETE", fmt.Sprintf("/api/v1/vms/%s/access/%d", vmName, userID), nil)
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
