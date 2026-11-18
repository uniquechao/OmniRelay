\set ON_ERROR_STOP on
\getenv app_id OMNIRELAY_EVIDENCE_APP_ID
\getenv tenant_key OMNIRELAY_EVIDENCE_TENANT_KEY
\getenv message_id OMNIRELAY_EVIDENCE_MESSAGE_ID
\getenv account_id OMNIRELAY_EVIDENCE_ACCOUNT_ID

BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL TIME ZONE 'UTC';
SET LOCAL statement_timeout = '5s';

WITH bot AS (
    SELECT credentials_generation, version, enabled, connection_generation,
           connection_state, last_heartbeat_at, last_message_at
    FROM feishu_bot_configs
    WHERE tenant_key = :'tenant_key' AND app_id = :'app_id'
)
SELECT to_char(clock_timestamp(), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS observed_at,
       credentials_generation, version, enabled, connection_generation, connection_state,
       to_char(last_heartbeat_at, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS last_heartbeat_at,
       to_char(last_message_at, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS last_message_at,
       (SELECT count(*) FROM feishu_bot_checks AS checks
        WHERE checks.tenant_key = :'tenant_key' AND checks.app_id = :'app_id'
          AND checks.credentials_generation < bot.credentials_generation
          AND checks.state IN ('queued', 'running')) AS old_active_checks
FROM bot;

SELECT count(event.message_id) AS event_count,
       count(work.id) AS work_count,
       count(work.id) FILTER (WHERE work.state = 'pending_route') AS pending_route_count,
       coalesce(bool_and(work.account_id = nullif(:'account_id', '')), false) AS matches_web_account,
       max(event.chat_kind) AS chat_kind,
       to_char(max(event.received_at), 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS received_at,
       max(event.config_version) AS event_config_version,
       max(event.connection_generation) AS event_connection_generation
FROM feishu_inbound_events AS event
LEFT JOIN feishu_inbound_work AS work USING (app_id, tenant_id, message_id)
WHERE event.tenant_id = :'tenant_key' AND event.app_id = :'app_id'
  AND event.message_id = :'message_id';

COMMIT;
