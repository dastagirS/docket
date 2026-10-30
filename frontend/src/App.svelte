<script lang="ts">
  import { onMount, tick } from "svelte";
  import Icon from "./Icon.svelte";
  import ProjectDialog from "./ProjectDialog.svelte";
  import TicketDialog from "./TicketDialog.svelte";
  import { api, ApiError, errorMessage, PAGE_SIZE, TICKETS_VISIBLE_MAX, projectPath, ticketPath, readPage, readProject, readTicket, readEvent, type Project, type Ticket } from "./api";

  const SEARCH_DELAY_MS = 300;
  const NOTIFICATION_DURATION_MS = 10000;
  const SYNC_INTERVAL_MS = 3000;
  const DRAG_DATA_TYPE = "application/x-docket-ticket";
  let projects: Project[] = [];
  let project: Project | null = null;
  let tickets: Ticket[] = [];
  let view: "board" | "list" = "board";
  let search = "";
  let cursor = 0;
  let more = false;
  let loadingMore = false;
  let loadingProject = false;
  let draggedTicket: Ticket | null = null;
  let dragGeneration = 0;
  let dropStatus: string | null = null;
  let movingTicketID: number | null = null;
  let showArchived = false;
  let showArchivedTickets = false;
  let syncing = false;
  let eventCursor = 0;
  let copyLink = "";
  let copyDialog: HTMLDialogElement;
  let copyInput: HTMLInputElement;
  $: visibleProjects = projects.filter((item) => item.archived === showArchived);
  $: activeCount = projects.filter((item) => !item.archived).length;
  $: archivedCount = projects.length - activeCount;
  let generation = 0;
  let notification = "";
  let searchTimer: ReturnType<typeof setTimeout> | undefined;
  let notificationTimer: ReturnType<typeof setTimeout> | undefined;
  let projectDialog: ProjectDialog;
  let ticketDialog: TicketDialog;

  function notify(message: string): void {
    notification = message;
    clearTimeout(notificationTimer);
    notificationTimer = setTimeout(() => { notification = ""; }, NOTIFICATION_DURATION_MS);
  }
  function run(action: () => Promise<unknown>): void {
    action().catch((error: unknown) => notify(errorMessage(error)));
  }
  function finishTicketDrag(): void {
    draggedTicket = null;
    dropStatus = null;
  }
  function startTicketDrag(event: DragEvent, ticket: Ticket): void {
    // An in-flight board read could replace the source node or its version during the native gesture.
    if (!project || ticket.project_id !== project.id || ticket.archived || draggedTicket || movingTicketID !== null || loadingProject || loadingMore || !event.dataTransfer) {
      event.preventDefault();
      return;
    }
    try {
      event.dataTransfer.effectAllowed = "move";
      // Keep ticket text out of the transferable payload if the drag leaves this app.
      event.dataTransfer.clearData();
      event.dataTransfer.setData(DRAG_DATA_TYPE, String(ticket.id));
      draggedTicket = { ...ticket };
      dragGeneration = generation;
    } catch {
      event.preventDefault();
      finishTicketDrag();
      notify("Dragging is unavailable. Open the ticket and use its Status field instead.");
    }
  }
  function dragOverStatus(event: DragEvent, status: string): void {
    if (!project || !draggedTicket || movingTicketID !== null || draggedTicket.project_id !== project.id || dragGeneration !== generation || !project.statuses.includes(status)) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = status === draggedTicket.status ? "none" : "move";
    dropStatus = status === draggedTicket.status ? null : status;
  }
  async function dropTicket(event: DragEvent, status: string): Promise<void> {
    event.preventDefault();
    const selected = draggedTicket;
    const currentGeneration = dragGeneration;
    finishTicketDrag();
    if (!project || !selected || selected.archived || movingTicketID !== null || selected.project_id !== project.id || currentGeneration !== generation || !project.statuses.includes(status) || event.dataTransfer?.getData(DRAG_DATA_TYPE) !== String(selected.id) || status === selected.status) return;
    const reference = `${project.key}-${selected.id}`;
    const visibleCount = tickets.length;
    movingTicketID = selected.id;
    let committed = false;
    try {
      const saved = await api(ticketPath(selected.project_id, selected.id), { method: "PATCH", headers: { "If-Match": `"${selected.version}"` }, body: JSON.stringify({ status }) }, readTicket);
      if (saved.id !== selected.id || saved.project_id !== selected.project_id || saved.status !== status) throw new Error("Unexpected ticket response. Reload to check the result.");
      committed = true;
      if (currentGeneration === generation && project?.id === selected.project_id) tickets = tickets.map((ticket) => ticket.id === saved.id ? saved : ticket);
      notify(`${reference} moved to ${status}.`);
    } catch (failure) {
      notify(errorMessage(failure) + (failure instanceof ApiError ? "" : " Reload to check the result before trying again."));
    } finally {
      if (currentGeneration === generation && project?.id === selected.project_id) {
        try { await loadProject(selected.project_id, visibleCount); }
        catch (failure) { notify((committed ? "Ticket moved, but board refresh failed. " : "Board refresh failed. ") + errorMessage(failure) + " Reload to check the result."); }
      }
      movingTicketID = null;
    }
  }
  async function loadProjects(): Promise<void> {
    const loaded: Project[] = [];
    let after = 0;
    // The server's 128-project bound fits in two pages.
    for (let i = 0; i < 2; i++) {
      const page = await api(`/api/projects?after=${after}`, {}, readPage(readProject));
      loaded.push(...page.items);
      if (page.items.length < PAGE_SIZE) break;
      after = page.next_cursor;
    }
    projects = loaded;
  }
  async function loadProject(projectID: number, visibleCount = PAGE_SIZE): Promise<boolean> {
    finishTicketDrag();
    const currentGeneration = ++generation;
    loadingProject = true;
    try {
      const archiveFilter = showArchivedTickets;
      // Capture the cursor before the snapshot so mutations racing its reads remain visible to the next poll.
      const feed = await api(`${projectPath(projectID)}/events?tail=true&limit=1`, {}, readPage(readEvent));
      if (currentGeneration !== generation) return false;
      const [loaded, firstPage] = await Promise.all([
        api(projectPath(projectID), {}, readProject),
        api(`${projectPath(projectID)}/tickets?archived=${archiveFilter}&q=${encodeURIComponent(search)}`, {}, readPage(readTicket)),
      ]);
      let page = firstPage;
      const loadedTickets = [...page.items];
      const pageCount = Math.min(Math.max(1, Math.ceil(visibleCount / PAGE_SIZE)), TICKETS_VISIBLE_MAX / PAGE_SIZE);
      for (let i = 1; i < pageCount && page.items.length === PAGE_SIZE; i++) {
        if (currentGeneration !== generation) return false;
        page = await api(`${projectPath(projectID)}/tickets?after=${page.next_cursor}&archived=${archiveFilter}&q=${encodeURIComponent(search)}`, {}, readPage(readTicket));
        loadedTickets.push(...page.items);
      }
      if (currentGeneration !== generation) return false;
      if (!project || project.id !== loaded.id || project.archived !== loaded.archived) showArchived = loaded.archived;
      project = loaded;
      tickets = loadedTickets;
      eventCursor = feed.next_cursor;
      cursor = page.next_cursor;
      more = page.items.length === PAGE_SIZE;
      document.title = `${loaded.name} · Docket`;
      return true;
    } finally { if (currentGeneration === generation) loadingProject = false; }
  }
  async function navigate(projectID: number): Promise<void> {
    if (!ticketDialog.close()) return;
    clearTimeout(searchTimer);
    search = "";
    if (await loadProject(projectID)) history.pushState({}, "", `/projects/${projectID}`);
  }
  async function refresh(): Promise<void> {
    await loadProjects();
    if (project) await loadProject(project.id, tickets.length);
  }
  async function copyProjectURL(): Promise<void> {
    if (!project) return;
    copyLink = new URL(`/projects/${project.id}`, location.origin).href;
    try {
      if (!navigator.clipboard) throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(copyLink);
      notify("Project URL copied.");
    } catch {
      copyDialog.showModal();
      await tick();
      copyInput.focus();
      copyInput.select();
    }
  }
  async function projectArchived(saved: Project): Promise<void> {
    const currentGeneration = generation;
    await loadProjects();
    if (currentGeneration === generation && project?.id === saved.id) await loadProject(saved.id, tickets.length);
  }
  async function syncLive(): Promise<void> {
    if (syncing || draggedTicket || movingTicketID !== null || document.hidden) return;
    syncing = true;
    const currentGeneration = generation;
    const selectedID = project?.id;
    try {
      await loadProjects();
      if (!selectedID || currentGeneration !== generation || draggedTicket || movingTicketID !== null) return;
      const events = await api(`${projectPath(selectedID)}/events?after=${eventCursor}`, {}, readPage(readEvent));
      if (currentGeneration !== generation || draggedTicket || movingTicketID !== null) return;
      if (events.items.length) await loadProject(selectedID, tickets.length);
      else eventCursor = events.next_cursor;
      if (project?.id === selectedID) await ticketDialog.sync(project);
    } finally { syncing = false; }
  }
  async function projectSaved(saved: Project): Promise<void> {
    await loadProjects();
    search = "";
    if (await loadProject(saved.id)) history.pushState({}, "", `/projects/${saved.id}`);
  }
  function searchChanged(): void {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => { if (project) run(() => loadProject(project!.id)); }, SEARCH_DELAY_MS);
  }
  async function ticketVisibility(archived: boolean): Promise<void> {
    if (showArchivedTickets === archived) return;
    showArchivedTickets = archived;
    if (project) await loadProject(project.id);
  }
  async function loadMore(): Promise<void> {
    if (!project || !more || loadingMore || tickets.length >= TICKETS_VISIBLE_MAX) return;
    const currentGeneration = generation;
    loadingMore = true;
    try {
      const page = await api(`${projectPath(project.id)}/tickets?after=${cursor}&archived=${showArchivedTickets}&q=${encodeURIComponent(search)}`, {}, readPage(readTicket));
      if (currentGeneration !== generation) return;
      const existing = new Set(tickets.map((ticket) => ticket.id));
      tickets = [...tickets, ...page.items.filter((ticket) => !existing.has(ticket.id))].slice(0, TICKETS_VISIBLE_MAX);
      cursor = page.next_cursor;
      more = page.items.length === PAGE_SIZE;
    } finally { loadingMore = false; }
  }
  async function start(): Promise<void> {
    await loadProjects();
    const match = location.pathname.match(/^\/projects\/(\d+)$/);
    if (match) await loadProject(Number(match[1]));
    else if (projects.length) {
      await loadProject(projects[0].id);
      history.replaceState({}, "", `/projects/${projects[0].id}`);
    }
  }
  function popstate(): void {
    if (!ticketDialog.close()) {
      if (project) history.pushState({}, "", `/projects/${project.id}`);
      return;
    }
    const match = location.pathname.match(/^\/projects\/(\d+)$/);
    if (match) run(() => loadProject(Number(match[1])));
    else location.reload();
  }
  onMount(() => {
    run(start);
    const syncTimer = setInterval(() => run(syncLive), SYNC_INTERVAL_MS);
    return () => { clearInterval(syncTimer); clearTimeout(searchTimer); clearTimeout(notificationTimer); generation++; };
  });
</script>

<svelte:window onpopstate={popstate} />
<aside class="sidebar">
  <a class="brand" href="/" aria-label="Docket home"><img class="brand-mark" src={`${import.meta.env.BASE_URL}favicon.svg?v=queue-1`} width="26" height="26" alt="" aria-hidden="true">docket<span class="local-badge">local</span></a>
  <div class="sidebar-label"><span>PROJECTS</span><button id="add-project" class="icon-button" aria-label="New project" onclick={() => projectDialog.open(null)}><Icon name="plus" /></button></div>
  <div class="project-scope" aria-label="Project visibility"><button id="active-projects" aria-pressed={!showArchived} onclick={() => { showArchived = false; }}>Active <span>{activeCount}</span></button><button id="archived-projects" aria-pressed={showArchived} onclick={() => { showArchived = true; }}>Archived <span>{archivedCount}</span></button></div>
  <nav id="projects" aria-label="Projects">
    {#each visibleProjects as item (item.id)}
      <button class:active={project?.id === item.id} class="project-link" title={item.name} onclick={() => run(() => navigate(item.id))}><span class="project-letter">{item.key.slice(0, 1)}</span><span class="project-label">{item.name}</span></button>
    {/each}
  </nav>
  <button id="sidebar-new" class="new-project-button" onclick={() => projectDialog.open(null)}><Icon name="plus" /> New project</button>
  <div class="sidebar-bottom"><span class="connection-dot"></span> Private workspace<br><small>No authentication · trusted network only</small></div>
</aside>
<main>
  <header class="header"><div class="breadcrumb"><span class="muted">Workspace</span><span class="slash">/</span><h1 id="project-name">{project?.name ?? "Overview"}</h1></div><div class="header-actions"><button id="copy-project-url" class="icon-button" title="Copy project URL" aria-label="Copy project URL" hidden={!project} onclick={() => run(copyProjectURL)}><Icon name="link" /></button><button id="project-settings" class="icon-button" title="Project settings" aria-label="Project settings" hidden={!project} onclick={() => projectDialog.open(project)}><Icon name="settings" /></button><button id="new-ticket" class="primary" hidden={!project} onclick={() => { if (project) run(() => ticketDialog.open(project!, null)); }}><Icon name="plus" /> New ticket</button></div></header>
  <div id="archived-banner" class="archived-banner" hidden={!project?.archived}><Icon name="archive" /> Archived project. Tickets, comments, and history are retained. Restore it to return it to active projects.</div>
  <div id="toolbar" class="toolbar" hidden={!project}>
    <div class="view-switch" aria-label="Ticket view"><button id="board-view" aria-pressed={view === "board"} onclick={() => { view = "board"; }}><Icon name="board" /> Board</button><button id="list-view" aria-pressed={view === "list"} onclick={() => { view = "list"; }}><Icon name="list" /> List</button></div>
    <div class="view-switch" aria-label="Ticket visibility"><button id="active-tickets" aria-pressed={!showArchivedTickets} onclick={() => run(() => ticketVisibility(false))}>Active</button><button id="archived-tickets" aria-pressed={showArchivedTickets} onclick={() => run(() => ticketVisibility(true))}><Icon name="archive" /> Archived</button></div>
    <span id="ticket-count" class="muted">{tickets.length}{more ? "+" : ""} {tickets.length === 1 && !more ? "ticket" : "tickets"}</span>
    <div class="toolbar-right"><label class="search-label"><Icon name="search" /><input id="search" type="search" placeholder="Search tickets…" aria-label="Search tickets" maxlength="200" bind:value={search} oninput={searchChanged}></label><button id="refresh" class="quiet" aria-label="Refresh tickets" onclick={() => run(refresh)}><Icon name="refresh" /> Refresh</button></div>
  </div>
  <section id="welcome" class="welcome" hidden={!!project}><span class="welcome-mark">d</span><h2>Your work, in one place.</h2><p>A small board. A clear next step.<br>Create a project to start keeping track.</p><button id="welcome-new" class="primary" onclick={() => projectDialog.open(null)}><Icon name="plus" /> Create a project</button><span class="welcome-note">Simple by design. Yours to host.</span></section>
  <section id="content" aria-label="Tickets" class={view} aria-busy={loadingProject || movingTicketID !== null} hidden={!project}>
    {#if project}
      {#if view === "board"}
        <p id="board-drag-help" class="sr-only">Drag a ticket to another status column, or open it and use the Status field. Archived tickets cannot be dragged.</p>
        {#each project.statuses as status, index (status)}
          {@const columnTickets = tickets.filter((ticket) => ticket.status === status)}
          <section class="column" class:drop-target={dropStatus === status} aria-label={status} ondragover={(event) => dragOverStatus(event, status)} ondragleave={(event) => { if (dropStatus === status && (!(event.relatedTarget instanceof Node) || !event.currentTarget.contains(event.relatedTarget))) dropStatus = null; }} ondrop={(event) => run(() => dropTicket(event, status))}>
            <div class="column-heading"><span class={`status-dot tone-${index % 5}`}></span><span>{status}</span><span class="count">{columnTickets.length}</span><button class="icon-button" aria-label={`New ticket in ${status}`} onclick={() => run(() => ticketDialog.open(project!, null, status))}><Icon name="plus" /></button></div>
            <!-- svelte-ignore a11y_no_noninteractive_tabindex (Native scrollports need focus for keyboard scrolling; this labelled region supplies its accessible context.) -->
            <div class="column-tickets" role="region" aria-label={`${status} tickets`} tabindex="0">
              {#each columnTickets as ticket (ticket.id)}
                <button class="ticket-card" class:dragging={draggedTicket?.id === ticket.id} data-ticket-id={ticket.id} draggable={!ticket.archived && movingTicketID === null && !loadingProject && !loadingMore} disabled={movingTicketID === ticket.id} aria-label={`${project.key}-${ticket.id}: ${ticket.title}`} aria-describedby={`board-drag-help${ticket.assignee ? ` card-assignee-${ticket.id}` : ""}${ticket.parent_id !== null ? ` card-parent-${ticket.id}` : ""}`} ondragstart={(event) => startTicketDrag(event, ticket)} ondragend={finishTicketDrag} onclick={() => { if (!draggedTicket && movingTicketID !== ticket.id) run(() => ticketDialog.open(project!, ticket.id)); }}>
                  <div class="card-top"><span class="mono">{project.key}-{ticket.id}</span><Icon name="arrow-up-right" /></div>
                  <div class="card-title">{ticket.title}</div>
                  {#if ticket.parent_id !== null}<span id={`card-parent-${ticket.id}`} class="card-parent">Subticket of {project.key}-{ticket.parent_id}</span>{/if}
                  <div class="card-footer"><Icon name="clock" />{new Date(ticket.updated_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}{#if ticket.assignee}<span id={`card-assignee-${ticket.id}`} class="card-assignee" title={`Assigned to ${ticket.assignee}`}><Icon name="user" /><span>{ticket.assignee}</span></span>{/if}</div>
                </button>
              {:else}<div class="column-empty">{dropStatus === status ? "Drop ticket here" : "No tickets here"}</div>{/each}
              {#if dropStatus === status && columnTickets.length}<div class="column-drop-hint" aria-hidden="true">Drop ticket here</div>{/if}
            </div>
          </section>
        {/each}
      {:else}
        <table class="list-table"><thead><tr><th>Ticket</th><th>Title</th><th class="assignee-column">Assignee</th><th>Status</th></tr></thead><tbody>
          {#each tickets as ticket (ticket.id)}
            <tr><td class="mono muted">{project.key}-{ticket.id}</td><td><button class="list-ticket" onclick={() => run(() => ticketDialog.open(project!, ticket.id))}>{ticket.title}</button>{#if ticket.parent_id !== null}<span class="card-parent">Subticket of {project.key}-{ticket.parent_id}</span>{/if}</td><td class="list-assignee">{ticket.assignee || "Unassigned"}</td><td><span class="list-status"><span class={`status-dot tone-${project.statuses.indexOf(ticket.status) % 5}`}></span>{ticket.status}</span></td></tr>
          {/each}
        </tbody></table>
        {#if !tickets.length}<p class="empty-activity">No tickets found. Create one or try another search.</p>{/if}
      {/if}
    {/if}
  </section>
  <div class="pagination"><button id="load-tickets" class="quiet" hidden={!more || tickets.length >= TICKETS_VISIBLE_MAX} disabled={loadingMore} onclick={() => run(loadMore)}>Load more tickets</button><span id="page-note" class="muted">{tickets.length >= TICKETS_VISIBLE_MAX ? "Showing up to 1,000 tickets. Narrow your search to find others." : ""}</span></div>
</main>
<div id="notification" role="status" aria-live="polite" hidden={!notification}>{notification}</div>
<dialog id="copy-link-dialog" class="project-dialog" bind:this={copyDialog}><form method="dialog"><div class="dialog-heading"><h2>Project URL</h2><button id="close-copy-link" class="icon-button" aria-label="Close project URL"><Icon name="close" /></button></div><p class="muted">Automatic copying is unavailable. Copy the selected link.</p><label>Project URL<input id="project-url" bind:this={copyInput} value={copyLink} readonly></label></form></dialog>
<ProjectDialog bind:this={projectDialog} onsaved={projectSaved} onarchived={projectArchived} {notify} />
<TicketDialog bind:this={ticketDialog} onsaved={async (projectID) => { if (project?.id === projectID) await loadProject(projectID, tickets.length); }} {notify} />
