package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func init() { appPollInterval = 5 * time.Millisecond }

type recorded struct{ method, path, auth, body string }

func testClient(t *testing.T, h http.HandlerFunc) (*VirtuosoClient, *recorded) {
	t.Helper()
	rec := &recorded{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*rec = recorded{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(b)}
		h(w, r)
	}))
	t.Cleanup(ts.Close)
	return NewVirtuosoClient(ts.URL, "test-key", false), rec
}

func TestK8sClientCalls(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/clusters"):
			w.WriteHeader(202)
			w.Write([]byte(`{"name":"prod","vm_name":"k8s-prod","status":"provisioning","size":"large","workers":[]}`))
		case r.Method == "POST":
			w.WriteHeader(202)
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/kubeconfig"):
			w.Write([]byte("apiVersion: v1\n"))
		case strings.HasSuffix(r.URL.Path, "/credentials"):
			w.Write([]byte(`{"argocd_password":"p","headlamp_token":"t"}`))
		case strings.HasSuffix(r.URL.Path, "/missing"):
			w.WriteHeader(404)
		default:
			w.Write([]byte(`{"name":"prod","status":"running","ip":"192.0.2.9"}`))
		}
	})

	cl, err := c.CreateK8sCluster(K8sCreateRequest{Name: "prod", Size: "large", ArgoCD: true, WorkerCount: 2})
	if err != nil || cl.VMName != "k8s-prod" {
		t.Fatalf("create = %+v, %v", cl, err)
	}
	var body map[string]any
	json.Unmarshal([]byte(rec.body), &body)
	if rec.auth != "Bearer test-key" || body["worker_count"] != float64(2) || body["argocd"] != true {
		t.Errorf("create request = %+v", *rec)
	}

	if err := c.ScaleK8sCluster("prod", 3); err != nil || rec.path != "/api/v1/apps/k8s/clusters/prod/scale" || !strings.Contains(rec.body, `"worker_count":3`) {
		t.Errorf("scale = %v %+v", err, *rec)
	}
	if err := c.InstallK8sAddon("prod", "k9s"); err != nil || !strings.Contains(rec.body, `"addon":"k9s"`) {
		t.Errorf("addon = %v %+v", err, *rec)
	}
	if kc, err := c.GetK8sKubeconfig("prod"); err != nil || !strings.HasPrefix(kc, "apiVersion") {
		t.Errorf("kubeconfig = %q, %v", kc, err)
	}
	if cr, err := c.GetK8sCredentials("prod"); err != nil || cr.ArgoCDPassword != "p" || cr.HeadlampToken != "t" {
		t.Errorf("credentials = %+v, %v", cr, err)
	}
	if got, err := c.GetK8sCluster("missing"); err != nil || got != nil {
		t.Errorf("a 404 must read as not found, got %+v, %v", got, err)
	}
	if err := c.DeleteK8sCluster("missing"); err != nil {
		t.Errorf("deleting a missing cluster must succeed, got %v", err)
	}
}

func TestK8sErrorsAreSurfaced(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		w.Write([]byte(`{"error":"Cluster name already exists","status":409}`))
	})
	if _, err := c.CreateK8sCluster(K8sCreateRequest{Name: "prod"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v", err)
	}
}

func TestWaitForK8sCluster(t *testing.T) {
	var calls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		switch {
		case n == 1:
			w.Write([]byte(`{"name":"prod","status":"provisioning","workers":[]}`))
		case n == 2:
			w.Write([]byte(`{"name":"prod","status":"running","ip":"192.0.2.9","workers":[{"name":"w1","status":"provisioning"}]}`))
		default:
			w.Write([]byte(`{"name":"prod","status":"running","ip":"192.0.2.9","workers":[{"name":"w1","status":"ready"}]}`))
		}
	})
	cl, err := c.WaitForK8sCluster("prod", 1, 5*time.Second)
	if err != nil || len(cl.Workers) != 1 || cl.Workers[0].Status != "ready" {
		t.Fatalf("wait = %+v, %v", cl, err)
	}
	if calls < 3 {
		t.Errorf("returned after %d polls; it must wait for the worker to be ready", calls)
	}

	slow, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"prod","status":"provisioning","workers":[]}`))
	})
	if _, err := slow.WaitForK8sCluster("prod", 0, 40*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("a cluster that never runs must time out, got %v", err)
	}
}

func TestDockerClientCalls(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			w.WriteHeader(202)
			w.Write([]byte(`{"name":"tools","vm_name":"docker-tools","status":"provisioning"}`))
		case "DELETE":
			w.Write([]byte(`{"ok":true}`))
		default:
			w.Write([]byte(`{"name":"tools","vm_name":"docker-tools","status":"running","ip":"192.0.2.7"}`))
		}
	})
	inst, err := c.CreateDockerInstance(DockerCreateRequest{Name: "tools", Size: "medium", DiskGB: 40})
	if err != nil || inst.VMName != "docker-tools" || rec.path != "/api/v1/apps/docker/instances" {
		t.Fatalf("create = %+v, %v, %+v", inst, err, *rec)
	}
	if got, err := c.WaitForDockerInstance("tools", time.Second); err != nil || got.IP != "192.0.2.7" {
		t.Errorf("wait = %+v, %v", got, err)
	}
	if err := c.DeleteDockerInstance("tools"); err != nil || rec.method != "DELETE" {
		t.Errorf("delete = %v %+v", err, *rec)
	}
}

func TestLaunchRequestCarriesPostTailscaleScript(t *testing.T) {
	data, _ := json.Marshal(LaunchRequest{Name: "n", OS: "ubuntu-24.04", PostTailscaleScript: "echo hi"})
	if !strings.Contains(string(data), `"post_tailscale_script":"echo hi"`) {
		t.Errorf("launch body = %s", data)
	}
	data, _ = json.Marshal(LaunchRequest{Name: "n", OS: "ubuntu-24.04"})
	if strings.Contains(string(data), "post_tailscale_script") {
		t.Errorf("an unset script must be omitted, got %s", data)
	}
}

func TestMapping(t *testing.T) {
	m := &K8sClusterResourceModel{}
	mapK8sCluster(&K8sClusterResponse{Name: "prod", VMName: "k8s-prod", Status: "running", IP: "192.0.2.9", Size: "large", ArgoCD: true,
		Workers: []K8sWorkerResponse{{Name: "a"}, {Name: "b"}}}, m)
	if m.ID.ValueString() != "prod" || m.WorkerCount.ValueInt64() != 2 || !m.ArgoCD.ValueBool() || m.Size.ValueString() != "large" {
		t.Errorf("mapped model = %+v", m)
	}
}

// Every resource schema must be valid for the framework, and the resources must
// be registered with the provider.
func TestSchemasAreValid(t *testing.T) {
	for _, newRes := range []func() resource.Resource{NewK8sClusterResource, NewDockerInstanceResource, NewVMResource} {
		r := newRes()
		resp := &resource.SchemaResponse{}
		r.Schema(context.Background(), resource.SchemaRequest{}, resp)
		if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
			t.Errorf("%T schema invalid: %v", r, diags)
		}
	}
	names := map[string]bool{}
	for _, f := range (&VirtuosoProvider{}).Resources(context.Background()) {
		resp := &resource.MetadataResponse{}
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "virtuoso"}, resp)
		names[resp.TypeName] = true
	}
	for _, want := range []string{"virtuoso_vm", "virtuoso_k8s_cluster", "virtuoso_docker_instance"} {
		if !names[want] {
			t.Errorf("resource %s is not registered", want)
		}
	}
}
