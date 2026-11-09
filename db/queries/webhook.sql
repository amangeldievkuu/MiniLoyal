-- name: InsertInboundEvent :execrows
-- Zero rows means LINE delivered this event before; skip it.
INSERT INTO webhook_inbound_events (tenant_id, app_id, line_webhook_event_id, event_type, redelivery)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: DeleteOldInboundEvents :execrows
DELETE FROM webhook_inbound_events WHERE received_at < now() - interval '7 days';
