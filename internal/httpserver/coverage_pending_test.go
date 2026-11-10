package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/api"
)

// pendingOps are spec operations whose handlers are still 501 stubs (none
// since Phase 4 replaced the coupon and stamp stubs). They are exempt from the coverage
// matrix only while they answer 501: TestPendingOperationsAreStubs fails as
// soon as one is implemented, so the implementing change must move it into
// tenantOps, memberOps, serverOps or staffOps with its fixtures.
var pendingOps = []string{}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestPendingOperationsAreStubs calls every pending operation without
// credentials on a server with no services. A stub answers 501
// not_implemented before it looks at the caller; a real handler answers 401
// or 503, which means the operation belongs in a coverage matrix now.
func TestPendingOperationsAreStubs(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	h := newTestServer(t).Handler()
	seen := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			id := opKey(op.OperationID)
			if !slices.Contains(pendingOps, id) {
				continue
			}
			seen[id] = true
			t.Run(id, func(t *testing.T) {
				target := pathParam.ReplaceAllStringFunc(path, func(string) string { return uuid.NewString() })
				var body *strings.Reader
				if op.RequestBody != nil {
					body = strings.NewReader("{}")
				} else {
					body = strings.NewReader("")
				}
				req := httptest.NewRequest(method, target, body)
				if op.RequestBody != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				if method != http.MethodGet {
					req.Header.Set("Idempotency-Key", uuid.NewString())
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				var e api.Error
				_ = json.Unmarshal(rec.Body.Bytes(), &e)
				if rec.Code != http.StatusNotImplemented || e.Code != "not_implemented" {
					t.Errorf("%s %s answers %d %q, not the 501 stub: it is implemented, so move %s from pendingOps "+
						"into tenantOps, memberOps, serverOps or staffOps with its fixtures", method, path, rec.Code, e.Code, id)
				}
			})
		}
	}
	for _, id := range pendingOps {
		if !seen[id] {
			t.Errorf("pendingOps entry %s is not in api/openapi.yaml", id)
		}
	}
}
