package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/cdrrazan/roost/internal/state"
)

// WorkerScriptName is the fixed name of the fallback Worker roost deploys.
const WorkerScriptName = "roost-maintenance"

// WorkerRoute is one Cloudflare Worker route (a pattern bound to a script).
type WorkerRoute struct {
	ID      string `json:"id,omitempty"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

// workerJS is the fallback Worker. It proxies every request to the origin
// and, only when the origin is unreachable (a thrown fetch, or a Cloudflare
// origin-connectivity status — 502/503/504 and the 52x/530 tunnel-down
// family that produces the 1033 page), answers with the branded page instead.
// A healthy response passes straight through, so the Worker is invisible while
// the stack is up. The page is injected as a JS string literal (%s).
const workerJS = `const PAGE = %s;
const OFFLINE = new Set([502, 503, 504, 520, 521, 522, 523, 524, 525, 526, 527, 530]);
addEventListener("fetch", (event) => { event.respondWith(handle(event.request)); });
async function handle(request) {
  try {
    const resp = await fetch(request);
    if (OFFLINE.has(resp.status)) return offline();
    return resp;
  } catch (e) {
    return offline();
  }
}
function offline() {
  return new Response(PAGE, {
    status: 503,
    headers: {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
      "retry-after": "30",
    },
  });
}
`

// BuildWorkerScript renders the fallback Worker with the given maintenance
// page embedded. The page is JSON-encoded so it becomes a valid, fully
// escaped JS string literal regardless of its contents.
func BuildWorkerScript(page []byte) string {
	lit, _ := json.Marshal(string(page))
	return fmt.Sprintf(workerJS, lit)
}

// PutWorkerScript uploads (creates or overwrites) a Worker script. The body
// is raw JS under application/javascript, not a JSON envelope.
func (c *Client) PutWorkerScript(accountID, name, script string) error {
	path := "/accounts/" + accountID + "/workers/scripts/" + name
	return c.doRaw(http.MethodPut, path, "application/javascript", []byte(script), nil)
}

// DeleteWorkerScript removes a Worker script from the account.
func (c *Client) DeleteWorkerScript(accountID, name string) error {
	return c.do(http.MethodDelete, "/accounts/"+accountID+"/workers/scripts/"+name, nil, nil)
}

// ListWorkerRoutes lists the Worker routes in a zone.
func (c *Client) ListWorkerRoutes(zoneID string) ([]WorkerRoute, error) {
	var routes []WorkerRoute
	if err := c.do(http.MethodGet, "/zones/"+zoneID+"/workers/routes", nil, &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

// CreateWorkerRoute binds a pattern to a script in a zone.
func (c *Client) CreateWorkerRoute(zoneID, pattern, script string) (WorkerRoute, error) {
	var created WorkerRoute
	body := map[string]string{"pattern": pattern, "script": script}
	if err := c.do(http.MethodPost, "/zones/"+zoneID+"/workers/routes", body, &created); err != nil {
		return WorkerRoute{}, err
	}
	created.Pattern = pattern
	created.Script = script
	return created, nil
}

// UpdateWorkerRoute repoints an existing route at a script.
func (c *Client) UpdateWorkerRoute(zoneID, id, pattern, script string) error {
	body := map[string]string{"pattern": pattern, "script": script}
	return c.do(http.MethodPut, "/zones/"+zoneID+"/workers/routes/"+id, body, nil)
}

// DeleteWorkerRoute removes a route from a zone.
func (c *Client) DeleteWorkerRoute(zoneID, id string) error {
	return c.do(http.MethodDelete, "/zones/"+zoneID+"/workers/routes/"+id, nil, nil)
}

// WorkerRouteSpec is a route to ensure: a pattern in a zone.
type WorkerRouteSpec struct {
	ZoneID  string
	Pattern string
}

// EnsureWorker uploads the fallback script and ensures a route for each spec,
// idempotently: an existing route with the same pattern is repointed at our
// script rather than duplicated (Cloudflare rejects a duplicate pattern). It
// returns the state.Worker the caller should persist so teardown can later
// remove exactly what was created.
func EnsureWorker(client *Client, accountID string, page []byte, specs []WorkerRouteSpec) (*state.Worker, error) {
	script := BuildWorkerScript(page)
	if err := client.PutWorkerScript(accountID, WorkerScriptName, script); err != nil {
		return nil, fmt.Errorf("upload worker script: %w", err)
	}

	worker := &state.Worker{ScriptName: WorkerScriptName}
	for _, spec := range specs {
		existing, err := client.ListWorkerRoutes(spec.ZoneID)
		if err != nil {
			return nil, fmt.Errorf("list worker routes: %w", err)
		}
		var found *WorkerRoute
		for i := range existing {
			if existing[i].Pattern == spec.Pattern {
				found = &existing[i]
				break
			}
		}
		if found != nil {
			if found.Script != WorkerScriptName {
				if err := client.UpdateWorkerRoute(spec.ZoneID, found.ID, spec.Pattern, WorkerScriptName); err != nil {
					return nil, fmt.Errorf("update worker route %s: %w", spec.Pattern, err)
				}
			}
			worker.Routes = append(worker.Routes, state.WorkerRoute{ID: found.ID, ZoneID: spec.ZoneID, Pattern: spec.Pattern})
			continue
		}
		created, err := client.CreateWorkerRoute(spec.ZoneID, spec.Pattern, WorkerScriptName)
		if err != nil {
			return nil, fmt.Errorf("create worker route %s: %w", spec.Pattern, err)
		}
		worker.Routes = append(worker.Routes, state.WorkerRoute{ID: created.ID, ZoneID: spec.ZoneID, Pattern: spec.Pattern})
	}
	return worker, nil
}

// TeardownWorker removes the routes and script recorded in st.Worker, then
// clears it. Like Teardown it only touches what roost recorded; a route that
// fails to delete is kept so a retry re-attempts just the leftovers.
func TeardownWorker(client *Client, st *state.State) error {
	if st.Worker == nil {
		return nil
	}
	var kept []state.WorkerRoute
	var errs []error
	for _, r := range st.Worker.Routes {
		if err := client.DeleteWorkerRoute(r.ZoneID, r.ID); err != nil {
			errs = append(errs, fmt.Errorf("worker route %s: %w", r.Pattern, err))
			kept = append(kept, r)
		}
	}
	if len(kept) > 0 {
		st.Worker.Routes = kept
		return errors.Join(errs...)
	}
	if err := client.DeleteWorkerScript(st.AccountID, st.Worker.ScriptName); err != nil {
		errs = append(errs, fmt.Errorf("worker script %s: %w", st.Worker.ScriptName, err))
		st.Worker.Routes = nil
		return errors.Join(errs...)
	}
	st.Worker = nil
	return errors.Join(errs...)
}
