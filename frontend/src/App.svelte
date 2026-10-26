<script lang="ts">
  import { onMount, tick } from "svelte";
  import Icon from "./Icon.svelte";
  import ProjectDialog from "./ProjectDialog.svelte";
  import TicketDialog from "./TicketDialog.svelte";
  import { api, errorMessage, PAGE_SIZE, TICKETS_VISIBLE_MAX, projectPath, readPage, readProject, readTicket, readEvent, type Project, type Ticket } from "./api";

  const SEARCH_DELAY_MS = 300;
  const NOTIFICATION_DURATION_MS = 10000;
  const SYNC_INTERVAL_MS = 3000;
  let projects: Project[] = [];
  let project: Project | null = null;
  let tickets: Ticket[] = [];
  let view: "board" | "list" = "board";
  let search = "";
  let cursor = 0;
  let more = false;
  let loadingMore = false;
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
    const currentGeneration = ++generation;
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
    if (syncing || document.hidden) return;
    syncing = true;
    const currentGeneration = generation;
    const selectedID = project?.id;
    try {
      await loadProjects();
      if (!selectedID || currentGeneration !== generation) return;
      const events = await api(`${projectPath(selectedID)}/events?after=${eventCursor}`, {}, readPage(readEvent));
      if (currentGeneration !== generation) return;
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
  <a class="brand" href="/" aria-label="Docket home"><span class="brand-mark">d</span>Docket<span class="local-badge">local</span></a>
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
  <section id="content" aria-label="Tickets" class={view} hidden={!project}>
    {#if project}
      {#if view === "board"}
        {#each project.statuses as status, index (status)}
          {@const columnTickets = tickets.filter((ticket) => ticket.status === status)}
          <section class="column" aria-label={status}>
            <div class="column-heading"><span class={`status-dot tone-${index % 5}`}></span><span>{status}</span><span class="count">{columnTickets.length}</span><button class="icon-button" aria-label={`New ticket in ${status}`} onclick={() => run(() => ticketDialog.open(project!, null, status))}><Icon name="plus" /></button></div>
            {#each columnTickets as ticket (ticket.id)}
              <button class="ticket-card" aria-label={`${project.key}-${ticket.id}: ${ticket.title}`} aria-describedby={ticket.assignee ? `card-assignee-${ticket.id}` : undefined} onclick={() => run(() => ticketDialog.open(project!, ticket.id))}>
                <div class="card-top"><span class="mono">{project.key}-{ticket.id}</span><Icon name="arrow-up-right" /></div>
                <div class="card-title">{ticket.title}</div>
                <div class="card-footer"><Icon name="clock" />{new Date(ticket.updated_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}{#if ticket.assignee}<span id={`card-assignee-${ticket.id}`} class="card-assignee" title={`Assigned to ${ticket.assignee}`}><Icon name="user" /><span>{ticket.assignee}</span></span>{/if}</div>
              </button>
            {:else}<div class="column-empty">No tickets here</div>{/each}
          </section>
        {/each}
      {:else}
        <table class="list-table"><thead><tr><th>Ticket</th><th>Title</th><th class="assignee-column">Assignee</th><th>Status</th></tr></thead><tbody>
          {#each tickets as ticket (ticket.id)}
            <tr><td class="mono muted">{project.key}-{ticket.id}</td><td><button class="list-ticket" onclick={() => run(() => ticketDialog.open(project!, ticket.id))}>{ticket.title}</button></td><td class="list-assignee">{ticket.assignee || "Unassigned"}</td><td><span class="list-status"><span class={`status-dot tone-${project.statuses.indexOf(ticket.status) % 5}`}></span>{ticket.status}</span></td></tr>
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
