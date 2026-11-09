-- +goose Up
-- Erasure must also blank the device details on consent records. The
-- application may update only those two columns; version and timestamp stay
-- as proof of consent.
GRANT UPDATE (ip, user_agent) ON member_consents TO miniloyal_app;

-- +goose Down
REVOKE UPDATE (ip, user_agent) ON member_consents FROM miniloyal_app;
