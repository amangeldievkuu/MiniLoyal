package audit

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amangeldievkuu/miniloyal/internal/principal"
	"github.com/amangeldievkuu/miniloyal/internal/testutil"
)

func TestRecordWritesEntryWithRequestInfo(t *testing.T) {
	d := testutil.NewDB(t)
	tenant := d.NewTenant(t, "Alpha")
	user := uuid.New()
	appID := uuid.New()
	ctx := WithRequestInfo(context.Background(), RequestInfo{RequestID: "req-1", IP: "203.0.113.9"})

	err := d.WithTenant(t, tenant, func(ctx2 context.Context, tx pgx.Tx) error {
		// Keep the request info from the outer context.
		return Record(WithRequestInfo(ctx2, RequestInfo{RequestID: "req-1", IP: "203.0.113.9"}), tx, Entry{
			TenantID:   tenant,
			AppID:      &appID,
			Actor:      principal.Principal{Kind: principal.KindUser, UserID: user},
			Action:     AppCreate,
			TargetType: "app",
			TargetID:   appID.String(),
			Metadata:   map[string]any{"name": "Cafe"},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx

	var actorType, action, targetID, requestID, ip, meta string
	var actorID, gotApp uuid.UUID
	err = d.Admin.QueryRow(context.Background(),
		"SELECT actor_type, actor_id, app_id, action, target_id, request_id, host(ip), metadata::text FROM audit_log WHERE tenant_id = $1", tenant).
		Scan(&actorType, &actorID, &gotApp, &action, &targetID, &requestID, &ip, &meta)
	if err != nil {
		t.Fatal(err)
	}
	if actorType != "user" || actorID != user || gotApp != appID || action != AppCreate || targetID != appID.String() || requestID != "req-1" || ip != "203.0.113.9" || meta != `{"name": "Cafe"}` {
		t.Errorf("unexpected row: %s %s %s %s %s %s %s %s", actorType, actorID, gotApp, action, targetID, requestID, ip, meta)
	}
}

func TestRecordRequiresActionAndTarget(t *testing.T) {
	if err := Record(context.Background(), nil, Entry{TenantID: uuid.New()}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestActorMapping(t *testing.T) {
	key := uuid.New()
	if actorType(principal.Principal{Kind: principal.KindAPIKey, APIKeyID: key}) != "api_key" || actorID(principal.Principal{Kind: principal.KindAPIKey, APIKeyID: key}) != key {
		t.Error("api key actor mapping wrong")
	}
	if actorType(principal.Principal{}) != "system" || actorID(principal.Principal{}) != uuid.Nil {
		t.Error("system actor mapping wrong")
	}
}
