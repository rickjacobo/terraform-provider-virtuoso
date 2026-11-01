package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// VMSchedule is a start and stop schedule for one VM.
type VMSchedule struct {
	VM         string `json:"vm"`
	Days       string `json:"days"`
	StartAt    string `json:"start_at"`
	StopAt     string `json:"stop_at"`
	TZ         string `json:"tz"`
	Enabled    bool   `json:"enabled"`
	LastAction string `json:"last_action,omitempty"`
	LastAt     string `json:"last_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// RestorePolicy takes automatic snapshots of a VM on a schedule.
type RestorePolicy struct {
	VM            string `json:"vm"`
	Days          string `json:"days"`
	At            string `json:"at"`
	TZ            string `json:"tz"`
	RetentionDays int64  `json:"retention_days"`
	Enabled       bool   `json:"enabled"`
	LastAt        string `json:"last_at,omitempty"`
	LastResult    string `json:"last_result,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type RestorePolicyEstimate struct {
	Copies int64 `json:"copies"`
	Bytes  int64 `json:"bytes_total"`
}

type restorePolicyEnvelope struct {
	Policy   RestorePolicy         `json:"policy"`
	Estimate RestorePolicyEstimate `json:"estimate"`
}

// Snapshot is one saved full copy of a VM's disk.
type Snapshot struct {
	ID            string `json:"id"`
	VM            string `json:"vm"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	ExpiresAt     string `json:"expires_at"`
	RetentionDays int64  `json:"retention_days"`
	SizeBytes     int64  `json:"size_bytes"`
	Consistency   string `json:"consistency"`
	Error         string `json:"error"`
}

type SnapshotCreateRequest struct {
	Name          string `json:"name,omitempty"`
	RetentionDays int64  `json:"retention_days"`
}

func vmPath(vm, suffix string) string { return "/api/v1/vms/" + url.PathEscape(vm) + suffix }

// putJSON sends a PUT that the API answers with 200 and decodes the reply.
func (c *VirtuosoClient) putJSON(path string, body, out interface{}) error {
	resp, err := c.doRequest("PUT", path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.parseError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// Schedules

func (c *VirtuosoClient) GetSchedule(vm string) (*VMSchedule, error) {
	var out VMSchedule
	found, err := c.getJSON(vmPath(vm, "/schedule"), &out)
	if err != nil || !found {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) PutSchedule(s VMSchedule) (*VMSchedule, error) {
	var out VMSchedule
	body := map[string]interface{}{"days": s.Days, "start_at": s.StartAt, "stop_at": s.StopAt, "tz": s.TZ, "enabled": s.Enabled}
	if err := c.putJSON(vmPath(s.VM, "/schedule"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) DeleteSchedule(vm string) error {
	return c.deleteIgnoringMissing(vmPath(vm, "/schedule"))
}

// Restore point policies

func (c *VirtuosoClient) GetRestorePolicy(vm string) (*RestorePolicy, *RestorePolicyEstimate, error) {
	var out restorePolicyEnvelope
	found, err := c.getJSON(vmPath(vm, "/restore-policy"), &out)
	if err != nil || !found {
		return nil, nil, err
	}
	return &out.Policy, &out.Estimate, nil
}

func (c *VirtuosoClient) PutRestorePolicy(p RestorePolicy) (*RestorePolicy, *RestorePolicyEstimate, error) {
	var out restorePolicyEnvelope
	body := map[string]interface{}{"days": p.Days, "at": p.At, "tz": p.TZ, "retention_days": p.RetentionDays, "enabled": p.Enabled}
	if err := c.putJSON(vmPath(p.VM, "/restore-policy"), body, &out); err != nil {
		return nil, nil, err
	}
	return &out.Policy, &out.Estimate, nil
}

func (c *VirtuosoClient) DeleteRestorePolicy(vm string) error {
	return c.deleteIgnoringMissing(vmPath(vm, "/restore-policy"))
}

// Snapshots

func (c *VirtuosoClient) ListSnapshots(vm string) ([]Snapshot, error) {
	var out struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	found, err := c.getJSON(vmPath(vm, "/snapshots"), &out)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return out.Snapshots, nil
}

// GetSnapshot returns one snapshot, or nil if it no longer exists (for example it expired).
func (c *VirtuosoClient) GetSnapshot(vm, id string) (*Snapshot, error) {
	list, err := c.ListSnapshots(vm)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, nil
}

func (c *VirtuosoClient) CreateSnapshot(vm string, req SnapshotCreateRequest) (*Snapshot, error) {
	var out Snapshot
	if err := c.postAccepted(vmPath(vm, "/snapshots"), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *VirtuosoClient) DeleteSnapshot(vm, id string) error {
	return c.deleteIgnoringMissing(vmPath(vm, "/snapshots/"+url.PathEscape(id)))
}

// WaitForSnapshot waits until the snapshot is ready. It returns an error if it fails.
func (c *VirtuosoClient) WaitForSnapshot(vm, id string, timeout time.Duration) (*Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		s, err := c.GetSnapshot(vm, id)
		if err != nil {
			return nil, err
		}
		if s == nil {
			return nil, fmt.Errorf("snapshot %s of %s disappeared while it was being created", id, vm)
		}
		switch s.Status {
		case "ready":
			return s, nil
		case "failed":
			return s, fmt.Errorf("snapshot %s of %s failed: %s", id, vm, s.Error)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for snapshot %s of %s", id, vm)
		}
		time.Sleep(appPollInterval)
	}
}

// normalizeDays turns the many ways to write a day list into one form, so that
// "Mon, Fri" in a configuration and "mon,fri" from the server are not seen as a change.
func normalizeDays(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "everyday", "every day", "daily":
		return "everyday"
	case "weekdays", "weekends":
		return s
	}
	order := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	set := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if len(part) > 3 {
			part = part[:3]
		}
		set[part] = true
	}
	var picked []string
	for _, d := range order {
		if set[d] {
			picked = append(picked, d)
			delete(set, d)
		}
	}
	if len(set) > 0 { // something unrecognised: leave it for the server to reject
		return s
	}
	joined := strings.Join(picked, ",")
	switch joined {
	case "mon,tue,wed,thu,fri":
		return "weekdays"
	case "sat,sun":
		return "weekends"
	case "mon,tue,wed,thu,fri,sat,sun":
		return "everyday"
	}
	return joined
}

// sameDays reports whether two day lists mean the same thing.
func sameDays(a, b string) bool { return normalizeDays(a) == normalizeDays(b) }

// validateDays rejects a days list that is not already in canonical form, so the
// configured value always equals what the server stores and Terraform never sees drift.
func validateDays(ctx context.Context, cfg tfsdk.Config, resp *resource.ValidateConfigResponse) {
	var days types.String
	resp.Diagnostics.Append(cfg.GetAttribute(ctx, path.Root("days"), &days)...)
	if days.IsNull() || days.IsUnknown() {
		return
	}
	if canon := normalizeDays(days.ValueString()); canon != days.ValueString() {
		resp.Diagnostics.AddAttributeError(path.Root("days"), "Write days in canonical form",
			fmt.Sprintf("Use %q instead of %q. Valid forms are everyday, weekdays, weekends, or lowercase three letter names in week order such as mon,wed,fri.", canon, days.ValueString()))
	}
}
