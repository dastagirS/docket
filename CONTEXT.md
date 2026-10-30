# Docket

A small, independent ticketing system for humans and API clients. External integrations add their own work orchestration and access policies without coupling the core to Pi or a particular agent framework.

## Language

**Project**: A grouping of tickets presented through a project board or list.

**Ticket**: A described unit of requested work tracked within a project.

**Subticket**: A ticket with one parent ticket in the same project. Only top-level tickets can be parents, and the parent link is fixed at creation. Subtickets remain visible as ordinary board/list items.

**Assignee**: An optional free-text label on a ticket. It is not an account, verified identity, or permission.

**Archived**: A reversible organizational state for a project or ticket. It removes the record from active UI views without deleting it, hiding its API routes, or making it read-only.

**Status**: A configurable progress state for a ticket.

**Comment**: An attributed contribution to a ticket's discussion.

**History**: The recorded changes to a ticket and their attribution.

**Change event**: A durable notification of a change that external integrations can consume.

**CLI**: A short-lived HTTP client of Docket's existing service. It shares API validation and history, never opens the database for client commands, and does not implement integration ownership/delegation policies.

**Folder binding**: A canonical folder-to-project/server association in the private user registry at `~/.docket/registry.json`, created by `project init`. It is a convenience, not authorization or a Pi ownership receipt.

**Integration**: An external client that uses Docket's API and events to add its own behavior.

**Owner**: The sole human operator of a Docket instance, not an authenticated account in v1.

## Relationships

- A **Project** contains **Tickets**.
- A top-level **Ticket** may have multiple **Subtickets**. Subtickets cannot have children. Status, assignee, comments, history, versions, and archiving are independent; there are no automatic rollups or archive cascades.
- A **Ticket** has a current **Status**, optional **Assignee**, **Comments**, and **History**.
- Projects and tickets may be **Archived** independently. Archiving a project preserves its tickets and their individual archive states.
- **Change events** let **Integrations** react without making the integration part of the ticketing system.
- The CLI **Folder binding** is separate from the Pi **Integration**'s repository pin, parent-session ownership, agent delegation, and assigned-ticket-only access. Updating the home registry never changes those integration policies or markers.

## Example dialogue

> **Developer:** Does moving a **Ticket** cause Docket to launch a worker?
> **User:** No. Docket records the change and exposes a durable **Change event**. Our separate **Integration** handles delegation.

## Flagged ambiguities

- "Board" previously referred to both the Pi session message board and the ticket interface. Docket's project board is distinct from Pi's message inbox.
- Authentication decision superseded: v1 has no accounts, password login, browser authentication sessions, or API keys. Authentication and project-level authorization are deferred to TODO.md.
- Anyone able to reach the unauthenticated server can access its exposed boards and operations. Project URLs identify boards; they are not access credentials.
- Repository integration scope resolved: pin each repo to its configured project URL, derive board identity from that configuration rather than agent input, reject cross-board ticket references, and preserve the scope for subagents. These are accidental-access guards, not security isolation against unrestricted shell/network access.
- Event delivery resolved: integrations consume a durable, cursor-based change feed, save their own position, and replay missed changes after downtime. No outgoing webhooks in v1.
- "Copy project" means copying its stable URL, not duplicating records. "Delete" currently means reversible archiving, for both projects and tickets; permanent deletion is not implemented.
- Live browser updates use bounded, visible-tab polling of project change events. Draft edits retain their original version and are never silently replaced by remote changes.

- A parent **Ticket** is a domain relationship, not the separate Pi integration's parent-session ownership. Reparenting existing tickets and deeper nesting are not supported.

- CLI initialization resolved: first init creates a project and records a folder binding in the home registry. Re-init displays counts including archived tickets and subtickets; the choices are keep (default), create another while preserving the old project, or archive old and replace. Archived keys remain reserved. Replacement is a journaled multi-step operation, not one cross-HTTP/filesystem transaction; incomplete work blocks implicit scope until explicitly reconciled.

- User-registry storage resolved: one private `~/.docket` directory per user, with a versioned `registry.json` and init recovery journal. Commands match the nearest canonical registered ancestor within the current Git boundary, never a global last-used board. Conflicting or pending registry state fails closed.
