# Identity, Access, and Directory Implementation Plan

**Goal:** Run an OmniRelay Go HTTP backend with Feishu login, scoped roles, and a synchronized organization directory.

**Architecture:** Use independent Go packages with package-level PostgreSQL adapters and an assembled HTTP server. Separate the proof of identity, permission checks, and directory data. Apply immutable versioned migrations before accepting traffic.

**Tech Stack:** Go, pgx, PostgreSQL, Feishu OAuth and Contacts API.

**Spec:** design/specs/omni-relay-go-service-design.md sections 8.1–8.5.

## Global Constraints

- Treat the single configured tenant and app as immutable process configuration. Keys, OAuth codes, tokens and browser session secrets stay out of published files, responses and logs.
- Bind browser login to one-time state, browser proof, PKCE and a preconfigured HTTPS callback. Keep revocable server sessions and enforce origin/token CSRF checks for writes.
- Grant management requires current authorization. Platform admins alone are insufficient for project resources. Projects require a trusted membership lookup. Feishu directory membership alone confers no role.
- Publish only complete, validated directory snapshots. Preserve an effective snapshot on transient fetch errors and hide revoked scope.

### Task 1: Identity

Own internal/identity and migrations/0001_identity.sql. Implement OAuth provider, secure login/session HTTP routes and storage, with replay, tenant, redirect and CSRF tests first. Export the principal and HTTP authentication adapter.

### Task 2: Role authorization and administration

Own internal/access and migrations/0002_access.sql. Implement defined platform/project roles, tenant and project checks, bootstrap, explicit account admission and account lifecycle, audited admin endpoints and atomic last-admin protection. Test role escalation, disabled accounts and cross-tenant access first.

### Task 3: Directory

Own internal/directory and migrations/0003_directory.sql. Implement Feishu Contacts, snapshot validation and publication, recurring/manual sync with PostgreSQL claiming, and secured queries. Test incomplete snapshots, multiple departments and scope loss first.

### Task 4: Assembly and end-to-end verification

Own go.mod, cmd/omnirelay, migrations embed/runner, configuration samples and integration tests. Wire identity, access and directory without test shortcuts. Run the suite and exercise OAuth and database paths with genuine PostgreSQL and simulated Feishu responses.
