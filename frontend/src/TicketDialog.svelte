<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import Icon from "./Icon.svelte";
  import { api, ApiError, errorMessage, ACTIVITY_VISIBLE_MAX, PAGE_SIZE, projectPath, ticketPath, readTicket, readPage, readComment, readEvent, readCommentResult, dateLabel, type Project, type Ticket, type Comment, type ChangeEvent } from "./api";

  export let onsaved: (projectID: number) => Promise<void>;
  export let notify: (message: string) => void;
  let dialog: HTMLDialogElement;
  let titleInput: HTMLTextAreaElement;
  let project: Project | null = null;
  let ticket: Ticket | null = null;
  let title = "";
  let description = "";
  let status = "";
  let commentBody = "";
  let comments: Comment[] = [];
  let history: ChangeEvent[] = [];
  let commentsMore = false;
  let historyMore = false;
  let commentsLoading = false;
  let historyLoading = false;
  let dirty = false;
  let pending = false;
  let ticketError = "";
  let commentError = "";
  let reload = false;
  let generation = 0;
  const descriptions: Record<string, string> = { "ticket.created": "created this ticket", "ticket.updated": "updated this ticket", "comment.created": "added a comment" };

  export function close(): boolean {
    if (pending) return false;
    if (dirty && !confirm("Discard unsaved ticket changes?")) return false;
    if (commentBody.trim() && !confirm("Discard your unsent comment?")) return false;
    generation++;
    dialog.close();
    dirty = false;
    commentBody = "";
    return true;
  }
  export async function open(selected: Project, ticketID: number | null, initialStatus = selected.statuses[0]): Promise<void> {
    if (!close()) return;
    const currentGeneration = ++generation;
    const loaded = ticketID === null ? null : await api(ticketPath(selected.id, ticketID), {}, readTicket);
    if (currentGeneration !== generation) return;
    project = selected;
    ticket = loaded;
    title = loaded?.title ?? "";
    description = loaded?.description ?? "";
    status = loaded?.status ?? initialStatus;
    comments = [];
    history = [];
    commentsMore = false;
    historyMore = false;
    commentsLoading = false;
    historyLoading = false;
    ticketError = "";
    commentError = "";
    reload = false;
    await tick();
    if (currentGeneration !== generation) return;
    dialog.showModal();
    titleInput.focus();
    if (ticket) await Promise.all([loadComments(), loadHistory()]);
  }
  async function loadComments(more = false): Promise<void> {
    if (!project || !ticket || commentsLoading) return;
    const previous = more ? comments : [];
    if (previous.length >= ACTIVITY_VISIBLE_MAX) return;
    const currentGeneration = generation;
    commentsLoading = true;
    try {
      const page = await api(`${ticketPath(project.id, ticket.id)}/comments?after=${previous.at(-1)?.id ?? 0}`, {}, readPage(readComment));
      if (currentGeneration !== generation) return;
      comments = [...previous, ...page.items].slice(0, ACTIVITY_VISIBLE_MAX);
      commentsMore = page.items.length === PAGE_SIZE && comments.length < ACTIVITY_VISIBLE_MAX;
    } finally { if (currentGeneration === generation) commentsLoading = false; }
  }
  async function loadHistory(more = false): Promise<void> {
    if (!project || !ticket || historyLoading) return;
    const previous = more ? history : [];
    if (previous.length >= ACTIVITY_VISIBLE_MAX) return;
    const currentGeneration = generation;
    historyLoading = true;
    try {
      const page = await api(`${ticketPath(project.id, ticket.id)}/history?after=${previous.at(-1)?.id ?? 0}`, {}, readPage(readEvent));
      if (currentGeneration !== generation) return;
      history = [...previous, ...page.items].slice(0, ACTIVITY_VISIBLE_MAX);
      historyMore = page.items.length === PAGE_SIZE && history.length < ACTIVITY_VISIBLE_MAX;
    } finally { if (currentGeneration === generation) historyLoading = false; }
  }
  function run(action: () => Promise<void>): void { action().catch((failure: unknown) => notify(errorMessage(failure))); }
  function failureMessage(failure: unknown): string {
    if (failure instanceof ApiError && failure.status === 409) reload = true;
    return errorMessage(failure) + (failure instanceof ApiError ? "" : " Reload to check the result before submitting again.");
  }
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending || !project) return;
    const selected = project;
    pending = true;
    ticketError = "";
    try {
      const editing = ticket !== null;
      const saved = await api(ticket ? ticketPath(selected.id, ticket.id) : `${projectPath(selected.id)}/tickets`, { method: ticket ? "PATCH" : "POST", headers: ticket ? { "If-Match": `"${ticket.version}"` } : {}, body: JSON.stringify({ title, description, status }) }, readTicket);
      ticket = saved;
      dirty = false;
      reload = false;
      if (!editing) dialog.close();
      await onsaved(selected.id);
      if (editing) await loadHistory();
    } catch (failure) {
      ticketError = failureMessage(failure);
      if (!dialog.open) notify(ticketError);
    } finally { pending = false; }
  }
  async function postComment(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending || !project || !ticket) return;
    if (dirty) { commentError = "Save or discard your ticket edits before posting a comment."; return; }
    pending = true;
    commentError = "";
    try {
      const result = await api(`${ticketPath(project.id, ticket.id)}/comments`, { method: "POST", headers: { "If-Match": `"${ticket.version}"` }, body: JSON.stringify({ body: commentBody }) }, readCommentResult);
      ticket = { ...ticket, version: result.ticket_version };
      commentBody = "";
      await Promise.all([loadComments(), loadHistory()]);
    } catch (failure) { commentError = failureMessage(failure); }
    finally { pending = false; }
  }
  function beforeunload(event: BeforeUnloadEvent): void {
    if (pending || dirty || commentBody.trim()) event.preventDefault();
  }
  onDestroy(() => { generation++; });
</script>

<svelte:window onbeforeunload={beforeunload} />
<dialog id="ticket-dialog" class="ticket-dialog" bind:this={dialog} oncancel={(event) => { event.preventDefault(); close(); }}>
  <div class="ticket-top"><span id="ticket-reference" class="mono muted">{ticket && project ? `${project.key}-${ticket.id}` : "NEW TICKET"}</span><button id="close-ticket" class="icon-button" aria-label="Close ticket" disabled={pending} onclick={close}><Icon name="close" /></button></div>
  <form id="ticket-form" oninput={() => { dirty = true; }} onsubmit={(event) => run(() => save(event))}>
    <label class="sr-only" for="ticket-title">Ticket title</label><textarea id="ticket-title" bind:this={titleInput} bind:value={title} class="ticket-title" rows="2" maxlength="200" required placeholder="What needs to happen?" disabled={pending}></textarea>
    <label class="status-label">Status<select id="ticket-status" bind:value={status} required disabled={pending}>{#each project?.statuses ?? [] as name}<option value={name}>{name}</option>{/each}</select></label>
    <label>Description<textarea id="ticket-description" bind:value={description} rows="7" maxlength="16000" placeholder="Add context, requirements, or acceptance criteria…" disabled={pending}></textarea><small>Plain text. URLs and code stay exactly as written.</small></label>
    <p id="ticket-error" class="form-error" role="alert">{ticketError}</p>
    <div class="form-actions"><button id="reload-ticket" class="quiet" type="button" hidden={!reload} disabled={pending} onclick={() => { if (project && ticket) run(() => open(project!, ticket!.id)); }}>Reload latest</button><button id="save-ticket" type="submit" class="primary" disabled={pending}>{ticket ? "Save changes" : "Create ticket"}</button></div>
  </form>
  <section id="discussion" hidden={!ticket}>
    <div class="section-heading"><h3>Discussion</h3><span class="muted">Comments are permanent</span></div>
    <div id="comments">
      {#each comments as comment (comment.id)}<div class="comment"><div class="comment-heading"><span>{comment.actor}</span><time datetime={comment.created_at}>{dateLabel(comment.created_at)}</time></div><p class="comment-body">{comment.body}</p></div>
      {:else}<p class="empty-activity">No updates yet. Start the conversation.</p>{/each}
      {#if comments.length >= ACTIVITY_VISIBLE_MAX}<p class="empty-activity">Showing the first 500 entries. The full record remains available through the API.</p>{/if}
    </div>
    <button id="load-comments" class="quiet" hidden={!commentsMore} disabled={commentsLoading || pending} onclick={() => run(() => loadComments(true))}>More comments</button>
    <form id="comment-form" onsubmit={(event) => run(() => postComment(event))}><label class="sr-only" for="comment-body">Comment</label><textarea id="comment-body" bind:value={commentBody} rows="3" required maxlength="16000" placeholder="Leave an update…" disabled={pending}></textarea><p id="comment-error" class="form-error" role="alert">{commentError}</p><div class="form-actions"><button id="post-comment" type="submit" class="quiet bordered" disabled={pending}>Post comment</button></div></form>
    <div class="section-heading"><h3>History</h3><span class="muted">Recorded with every change</span></div>
    <div id="history">
      {#each history as item (item.id)}<div class="history-item"><span>{item.actor} {descriptions[item.kind] ?? item.kind}{item.data.status ? ` · ${item.data.status}` : ""}</span><time datetime={item.created_at}>{dateLabel(item.created_at)}</time></div>
      {:else}<p class="empty-activity">No history yet.</p>{/each}
      {#if history.length >= ACTIVITY_VISIBLE_MAX}<p class="empty-activity">Showing the first 500 entries. The full record remains available through the API.</p>{/if}
    </div>
    <button id="load-history" class="quiet" hidden={!historyMore} disabled={historyLoading || pending} onclick={() => run(() => loadHistory(true))}>More history</button>
  </section>
</dialog>
