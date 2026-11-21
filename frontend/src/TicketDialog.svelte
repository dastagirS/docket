<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import Icon from "./Icon.svelte";
  import { renderDescription } from "./markdown";
  import { api, ApiError, errorMessage, ACTIVITY_VISIBLE_MAX, ASSIGNEE_BYTES_MAX, ATTACHMENTS_MAX, ATTACHMENT_BYTES_MAX, ATTACHMENT_TYPES, COMMENT_CHARACTERS_MAX, PAGE_SIZE, TICKET_LABELS_MAX, TICKETS_VISIBLE_MAX, attachmentsPath, projectPath, ticketPath, uploadAttachment, readTicket, readPage, readAttachment, readComment, readEvent, readCommentResult, dateLabel, type Project, type Ticket, type Attachment, type Comment, type ChangeEvent } from "./api";

  export let onsaved: (projectID: number) => Promise<void>;
  export let notify: (message: string) => void;
  let dialog: HTMLDialogElement;
  let titleInput: HTMLTextAreaElement;
  let project: Project | null = null;
  let ticket: Ticket | null = null;
  let title = "";
  let description = "";
  let descriptionInput: HTMLTextAreaElement;
  let descriptionEditing = true;
  let attachments: Attachment[] = [];
  let attachmentsLoading = false;
  let attachmentUploading = false;
  let attachmentError = "";
  let fileInput: HTMLInputElement;
  $: attachmentPaths = attachments.map(attachmentPath).filter((value) => value !== "");
  $: descriptionHTML = descriptionEditing ? "" : renderDescription(description, attachmentPaths);
  $: commentCharacters = Array.from(commentBody).length;
  $: commentTooLong = commentCharacters > COMMENT_CHARACTERS_MAX;
  let status = "";
  let assignee = "";
  let labels: string[] = [];
  let parentID: number | null = null;
  let subtickets: Ticket[] = [];
  let subticketsArchived = false;
  let subticketsMore = false;
  let subticketsLoading = false;
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
  let observedVersion = 0;
  const descriptions: Record<string, string> = { "ticket.created": "created this ticket", "ticket.updated": "updated this ticket", "comment.created": "added a comment", "attachment.created": "attached an image", "attachment.deleted": "removed an image" };

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
  export async function open(selected: Project, ticketID: number | null, initialStatus = selected.statuses[0], initialParentID: number | null = null): Promise<void> {
    if (!close()) return;
    const currentGeneration = ++generation;
    const loaded = ticketID === null ? null : await api(ticketPath(selected.id, ticketID), {}, readTicket);
    if (currentGeneration !== generation) return;
    project = selected;
    ticket = loaded;
    observedVersion = loaded?.version ?? 0;
    title = loaded?.title ?? "";
    description = loaded?.description ?? "";
    descriptionEditing = loaded === null;
    status = loaded?.status ?? initialStatus;
    assignee = loaded?.assignee ?? "";
    labels = [...(loaded?.labels ?? [])];
    parentID = loaded?.parent_id ?? initialParentID;
    subtickets = [];
    subticketsArchived = false;
    subticketsMore = false;
    subticketsLoading = false;
    comments = [];
    history = [];
    commentsMore = false;
    historyMore = false;
    commentsLoading = false;
    historyLoading = false;
    attachments = [];
    attachmentsLoading = false;
    attachmentUploading = false;
    attachmentError = "";
    ticketError = "";
    commentError = "";
    reload = false;
    await tick();
    if (currentGeneration !== generation) return;
    dialog.showModal();
    titleInput.focus();
    if (ticket) await Promise.all([loadComments(), loadHistory(), loadSubtickets(), loadAttachments()]);
  }
  export async function sync(selected: Project): Promise<void> {
    if (!dialog.open || pending || commentsLoading || historyLoading || subticketsLoading || !ticket || project?.id !== selected.id) return;
    const currentGeneration = generation;
    const loaded = await api(ticketPath(selected.id, ticket.id), {}, readTicket);
    if (currentGeneration !== generation || pending || commentsLoading || historyLoading || subticketsLoading) return;
    project = selected;
    if (loaded.parent_id === null) await loadSubtickets(false, subtickets.length);
    if (currentGeneration !== generation || pending) return;
    if (loaded.version === observedVersion) return;
    if (dirty) {
      reload = true;
      ticketError = "This ticket changed elsewhere. Your unsaved edits are preserved; reload latest before saving.";
    } else {
      ticket = loaded;
      title = loaded.title;
      description = loaded.description;
      status = loaded.status;
      assignee = loaded.assignee;
      labels = [...loaded.labels];
      reload = false;
      ticketError = "";
    }
    await Promise.all([loadComments(false, comments.length), loadHistory(false, history.length), loadAttachments()]);
    if (currentGeneration === generation) observedVersion = loaded.version;
  }
  async function loadSubtickets(more = false, visibleCount = PAGE_SIZE): Promise<void> {
    if (!project || !ticket || ticket.parent_id !== null || subticketsLoading) return;
    const previous = more ? subtickets : [];
    if (previous.length >= TICKETS_VISIBLE_MAX) return;
    const currentGeneration = generation;
    const selectedParentID = ticket.id;
    const selectedArchive = subticketsArchived;
    subticketsLoading = true;
    try {
      const path = `${projectPath(project.id)}/tickets?parent_id=${selectedParentID}&archived=${selectedArchive}`;
      let page = await api(`${path}&after=${previous.at(-1)?.id ?? 0}`, {}, readPage(readTicket));
      const loaded = [...previous, ...page.items];
      const pageCount = more ? 1 : Math.min(Math.max(1, Math.ceil(visibleCount / PAGE_SIZE)), TICKETS_VISIBLE_MAX / PAGE_SIZE);
      for (let i = 1; i < pageCount && page.items.length === PAGE_SIZE; i++) {
        if (currentGeneration !== generation) return;
        page = await api(`${path}&after=${page.next_cursor}`, {}, readPage(readTicket));
        loaded.push(...page.items);
      }
      if (currentGeneration !== generation) return;
      subtickets = loaded.slice(0, TICKETS_VISIBLE_MAX);
      subticketsMore = page.items.length === PAGE_SIZE && subtickets.length < TICKETS_VISIBLE_MAX;
    } finally { if (currentGeneration === generation) subticketsLoading = false; }
  }
  async function showSubtickets(archived: boolean): Promise<void> {
    if (subticketsLoading || pending) return;
    subticketsArchived = archived;
    await loadSubtickets();
  }
  async function loadComments(more = false, visibleCount = PAGE_SIZE): Promise<void> {
    if (!project || !ticket || commentsLoading) return;
    const previous = more ? comments : [];
    if (previous.length >= ACTIVITY_VISIBLE_MAX) return;
    const currentGeneration = generation;
    commentsLoading = true;
    try {
      const path = `${ticketPath(project.id, ticket.id)}/comments`;
      let page = await api(`${path}?after=${previous.at(-1)?.id ?? 0}`, {}, readPage(readComment));
      const loaded = [...previous, ...page.items];
      const pageCount = more ? 1 : Math.min(Math.max(1, Math.ceil(visibleCount / PAGE_SIZE)), ACTIVITY_VISIBLE_MAX / PAGE_SIZE);
      for (let i = 1; i < pageCount && page.items.length === PAGE_SIZE; i++) {
        if (currentGeneration !== generation) return;
        page = await api(`${path}?after=${page.next_cursor}`, {}, readPage(readComment));
        loaded.push(...page.items);
      }
      if (currentGeneration !== generation) return;
      comments = loaded.slice(0, ACTIVITY_VISIBLE_MAX);
      commentsMore = page.items.length === PAGE_SIZE && comments.length < ACTIVITY_VISIBLE_MAX;
    } finally { if (currentGeneration === generation) commentsLoading = false; }
  }
  async function loadHistory(more = false, visibleCount = PAGE_SIZE): Promise<void> {
    if (!project || !ticket || historyLoading) return;
    const previous = more ? history : [];
    if (previous.length >= ACTIVITY_VISIBLE_MAX) return;
    const currentGeneration = generation;
    historyLoading = true;
    try {
      const path = `${ticketPath(project.id, ticket.id)}/history`;
      let page = await api(`${path}?after=${previous.at(-1)?.id ?? 0}`, {}, readPage(readEvent));
      const loaded = [...previous, ...page.items];
      const pageCount = more ? 1 : Math.min(Math.max(1, Math.ceil(visibleCount / PAGE_SIZE)), ACTIVITY_VISIBLE_MAX / PAGE_SIZE);
      for (let i = 1; i < pageCount && page.items.length === PAGE_SIZE; i++) {
        if (currentGeneration !== generation) return;
        page = await api(`${path}?after=${page.next_cursor}`, {}, readPage(readEvent));
        loaded.push(...page.items);
      }
      if (currentGeneration !== generation) return;
      history = loaded.slice(0, ACTIVITY_VISIBLE_MAX);
      historyMore = page.items.length === PAGE_SIZE && history.length < ACTIVITY_VISIBLE_MAX;
    } finally { if (currentGeneration === generation) historyLoading = false; }
  }
  function run(action: () => Promise<void>): void { action().catch((failure: unknown) => notify(errorMessage(failure))); }
  // The dialog renders its own origin's attachment path; the server URL may use a public host.
  function attachmentPath(attachment: Attachment): string {
    try { return new URL(attachment.url, window.location.origin).pathname; } catch { return ""; }
  }
  async function loadAttachments(): Promise<void> {
    if (!project || !ticket || attachmentsLoading) return;
    const currentGeneration = generation;
    attachmentsLoading = true;
    try {
      const page = await api(`${attachmentsPath(project.id, ticket.id)}?limit=${ATTACHMENTS_MAX}`, {}, readPage(readAttachment));
      if (currentGeneration !== generation) return;
      attachments = page.items;
    } catch (failure) {
      if (currentGeneration === generation) attachmentError = failureMessage(failure);
    } finally { if (currentGeneration === generation) attachmentsLoading = false; }
  }
  function insertAttachment(attachment: Attachment): void {
    // Brackets in a display name would end the Markdown alt text early.
    const reference = `![${attachment.filename.replace(/[[\]]/g, "")}](${attachmentPath(attachment)})`;
    if (!description.includes(reference)) {
      const trimmed = description.trimEnd();
      description = trimmed === "" ? `${reference}\n` : `${trimmed}\n\n${reference}\n`;
      dirty = true;
    }
  }
  async function addAttachment(files: ArrayLike<File> | null | undefined): Promise<void> {
    if (pending || attachmentUploading || !project || !ticket) return;
    const candidates = Array.from(files ?? []);
    if (candidates.length === 0) return;
    const remaining = ATTACHMENTS_MAX - attachments.length;
    if (candidates.length > remaining) {
      attachmentError = `A ticket can hold ${ATTACHMENTS_MAX} images; ${remaining} slot(s) remain.`;
      return;
    }
    attachmentUploading = true;
    attachmentError = "";
    try {
      for (const file of candidates) {
        if (!ATTACHMENT_TYPES.includes(file.type)) { attachmentError = "Only PNG, JPEG, or GIF images are supported."; continue; }
        if (file.size > ATTACHMENT_BYTES_MAX) { attachmentError = "Images must be 5 MiB or smaller."; continue; }
        const saved = await uploadAttachment(project.id, ticket.id, file);
        attachments = [...attachments, saved];
        insertAttachment(saved);
      }
      await loadHistory();
    } catch (failure) { attachmentError = failureMessage(failure); }
    finally {
      attachmentUploading = false;
      if (fileInput) fileInput.value = "";
    }
  }
  async function removeAttachment(attachment: Attachment): Promise<void> {
    if (pending || attachmentUploading || !project || !ticket) return;
    if (!confirm(`Remove ${attachment.filename}? Its bytes are deleted from the database.`)) return;
    attachmentUploading = true;
    attachmentError = "";
    try {
      await api(`${attachmentsPath(project.id, ticket.id)}/${attachment.id}`, { method: "DELETE" }, () => undefined);
      attachments = attachments.filter((item) => item.id !== attachment.id);
      const path = attachmentPath(attachment);
      const reference = path === "" ? null : new RegExp(`!\\[[^\\]]*\\]\\(${path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\)\\n?`, "g");
      if (reference?.test(description)) { description = description.replace(reference, ""); dirty = true; }
      await loadHistory();
      notify("Image removed. The rest of the ticket was not changed.");
    } catch (failure) { attachmentError = failureMessage(failure); }
    finally { attachmentUploading = false; }
  }
  function attachFromClipboard(event: ClipboardEvent): void {
    const files = Array.from(event.clipboardData?.files ?? []).filter((file) => ATTACHMENT_TYPES.includes(file.type));
    if (files.length === 0) return;
    event.preventDefault();
    run(() => addAttachment(files));
  }
  function failureMessage(failure: unknown): string {
    if (failure instanceof ApiError && failure.status === 409) reload = true;
    return errorMessage(failure) + (failure instanceof ApiError ? "" : " Reload to check the result before submitting again.");
  }
  function toggleLabel(name: string): void {
    if (pending) return;
    if (labels.includes(name)) {
      labels = labels.filter((label) => label !== name);
      ticketError = "";
      dirty = true;
      return;
    }
    if (labels.length >= TICKET_LABELS_MAX) {
      ticketError = `A ticket can carry at most ${TICKET_LABELS_MAX} labels.`;
      return;
    }
    labels = [...labels, name];
    ticketError = "";
    dirty = true;
  }
  async function editDescription(): Promise<void> {
    if (pending || descriptionEditing) return;
    descriptionEditing = true;
    await tick();
    descriptionInput.focus();
  }
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending || !project) return;
    const selected = project;
    pending = true;
    ticketError = "";
    try {
      const editing = ticket !== null;
      const saved = await api(ticket ? ticketPath(selected.id, ticket.id) : `${projectPath(selected.id)}/tickets`, { method: ticket ? "PATCH" : "POST", headers: ticket ? { "If-Match": `"${ticket.version}"` } : {}, body: JSON.stringify({ title, description, status, assignee, labels, ...(!ticket ? { parent_id: parentID } : {}) }) }, readTicket);
      ticket = saved;
      assignee = saved.assignee;
      descriptionEditing = false;
      observedVersion = saved.version;
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
    if (commentTooLong) { commentError = `Comments must be at most ${COMMENT_CHARACTERS_MAX.toLocaleString()} characters.`; return; }
    if (dirty) { commentError = "Save or discard your ticket edits before posting a comment."; return; }
    pending = true;
    commentError = "";
    try {
      const result = await api(`${ticketPath(project.id, ticket.id)}/comments`, { method: "POST", headers: { "If-Match": `"${ticket.version}"` }, body: JSON.stringify({ body: commentBody }) }, readCommentResult);
      ticket = { ...ticket, version: result.ticket_version };
      observedVersion = result.ticket_version;
      commentBody = "";
      await Promise.all([loadComments(), loadHistory()]);
    } catch (failure) { commentError = failureMessage(failure); }
    finally { pending = false; }
  }
  async function toggleArchive(): Promise<void> {
    if (pending || !project || !ticket) return;
    if (dirty) { ticketError = "Save or discard your ticket edits before archiving or restoring."; return; }
    const selected = project;
    const current = ticket;
    if (!current.archived && !confirm(`Archive ${selected.key}-${current.id}? Its description, comments, and history will be kept.`)) return;
    pending = true;
    ticketError = "";
    try {
      const saved = await api(ticketPath(selected.id, current.id), { method: "PATCH", headers: { "If-Match": `"${current.version}"` }, body: JSON.stringify({ archived: !current.archived }) }, readTicket);
      ticket = saved;
      observedVersion = saved.version;
      reload = false;
      await onsaved(selected.id);
      await loadHistory(false, history.length);
      notify(saved.archived ? "Ticket archived. Nothing was deleted." : "Ticket restored.");
    } catch (failure) { ticketError = failureMessage(failure); }
    finally { pending = false; }
  }
  function beforeunload(event: BeforeUnloadEvent): void {
    if (pending || dirty || commentBody.trim()) event.preventDefault();
  }
  onDestroy(() => { generation++; });
</script>

<svelte:window onbeforeunload={beforeunload} />
<dialog id="ticket-dialog" class="ticket-dialog" bind:this={dialog} oncancel={(event) => { event.preventDefault(); close(); }}>
  <div class="ticket-top"><span id="ticket-reference" class="mono muted">{ticket && project ? `${project.key}-${ticket.id}` : "NEW TICKET"}</span><div class="ticket-top-actions"><button id="archive-ticket" class="quiet" hidden={!ticket} disabled={pending} onclick={() => run(toggleArchive)}><Icon name={ticket?.archived ? "restore" : "archive"} />{ticket?.archived ? "Restore" : "Archive"}</button><button id="close-ticket" class="icon-button" aria-label="Close ticket" disabled={pending} onclick={close}><Icon name="close" /></button></div></div>
  <div class="ticket-body" id="ticket-body">
  <p id="ticket-archived-banner" class="archived-banner" hidden={!ticket?.archived}><Icon name="archive" /> Archived ticket. Its description, comments, and history are retained.</p>
  {#if parentID !== null && project}
    <div class="ticket-parent"><span class="muted">Subticket of</span><button id="ticket-parent" class="quiet bordered" disabled={pending} onclick={() => run(() => open(project!, parentID))}>{project.key}-{parentID}<Icon name="arrow-up-right" /></button></div>
  {/if}
  <form id="ticket-form" oninput={() => { dirty = true; }} onsubmit={(event) => run(() => save(event))}>
    <label class="sr-only" for="ticket-title">Ticket title</label><textarea id="ticket-title" bind:this={titleInput} bind:value={title} class="ticket-title" rows="2" maxlength="200" required placeholder="What needs to happen?" disabled={pending}></textarea>
    <label class="status-label">Status<select id="ticket-status" bind:value={status} required disabled={pending}>{#each project?.statuses ?? [] as name}<option value={name}>{name}</option>{/each}</select></label>
    <label>Assignee<input id="ticket-assignee" bind:value={assignee} maxlength={ASSIGNEE_BYTES_MAX} placeholder="Unassigned" autocomplete="off" disabled={pending}><small>Free-text name, no account required. Leave empty to unassign.</small></label>
    <div class="label-field">
      <span class="field-label" id="ticket-labels-label">Labels</span>
      {#if project?.labels.length}
        <div class="label-picker" role="group" aria-labelledby="ticket-labels-label">
          {#each project.labels as name (name)}<button type="button" class="label-chip selectable" class:selected={labels.includes(name)} aria-pressed={labels.includes(name)} disabled={pending} onclick={() => toggleLabel(name)}>{name}</button>{/each}
        </div>
        <small>Pick from this project's labels (up to {TICKET_LABELS_MAX}). Change the list in project settings.</small>
      {:else}
        <small class="muted">No labels defined. Add them in project settings, then tickets can use them.</small>
      {/if}
    </div>
    <div class="description-field">
      <label id="description-label" for="ticket-description">Description</label>
      <div class="description-view" hidden={descriptionEditing}>
        <button id="edit-description" class="description-edit-target" type="button" aria-label="Edit description" disabled={pending} onfocus={() => run(editDescription)} onclick={() => run(editDescription)}></button>
        <div id="description-preview" class="markdown-content" aria-labelledby="description-label">{#if description}{@html descriptionHTML}{:else}<p class="muted">Add context, requirements, or acceptance criteria…</p>{/if}</div>
      </div>
      <textarea id="ticket-description" bind:this={descriptionInput} bind:value={description} hidden={!descriptionEditing} rows="7" maxlength="16000" placeholder="Add context, requirements, or acceptance criteria…" disabled={pending} onpaste={attachFromClipboard}></textarea>
      <small>{descriptionEditing ? "Markdown supported. Save changes to render your description." : "Click or focus the description to edit Markdown."}</small>
    </div>
  </form>
  <section id="attachments" class="attachments" aria-label="Images" aria-busy={attachmentsLoading || attachmentUploading} ondragover={(event) => event.preventDefault()} ondrop={(event) => { event.preventDefault(); run(() => addAttachment(event.dataTransfer?.files)); }}>
    <div class="section-heading"><h3>Images <span class="muted">{attachments.length}</span></h3>
      {#if ticket}<button id="add-image" class="quiet bordered" type="button" disabled={pending || attachmentUploading || attachments.length >= ATTACHMENTS_MAX} onclick={() => fileInput?.click()}><Icon name="plus" /> Add image</button>{/if}
    </div>
    {#if ticket}
      <input id="image-input" class="sr-only" type="file" bind:this={fileInput} accept={ATTACHMENT_TYPES.join(",")} multiple onchange={(event) => run(() => addAttachment(event.currentTarget.files))} />
      <div class="attachment-grid">
        {#each attachments as attachment (attachment.id)}
          <figure class="attachment" data-attachment-id={attachment.id}>
            <img src={attachmentPath(attachment)} alt={attachment.filename} loading="lazy" />
            <figcaption><span class="attachment-name">{attachment.filename}</span><span class="muted">{attachment.bytes < 1024 ? `${attachment.bytes} B` : `${Math.round(attachment.bytes / 1024)} KB`}</span>
              <span class="attachment-actions"><button type="button" class="quiet" disabled={pending || attachmentUploading} onclick={() => insertAttachment(attachment)}>Insert</button><button type="button" class="quiet" aria-label={`Remove ${attachment.filename}`} disabled={pending || attachmentUploading} onclick={() => run(() => removeAttachment(attachment))}><Icon name="trash" /></button></span>
            </figcaption>
          </figure>
        {:else}<p class="empty-activity">{attachmentsLoading ? "Loading images…" : "No images yet. Drop a PNG, JPEG, or GIF here, or paste one into the description."}</p>{/each}
      </div>
      <p id="attachment-error" class="form-error" role="alert">{attachmentError}</p>
      <small>Images are stored inside the Docket database, so one backup file stays complete. Up to {ATTACHMENTS_MAX} per ticket, 5 MiB each.</small>
    {:else}
      <small class="muted">Save this ticket first, then images can be attached.</small>
    {/if}
  </section>
  {#if ticket && parentID === null && project}
    <section id="subtickets" class="subtickets" aria-label="Subtickets" aria-busy={subticketsLoading}>
      <div class="section-heading"><h3>Subtickets <span class="muted">{subtickets.length}{subticketsMore ? "+" : ""}</span></h3><button id="add-subticket" class="quiet bordered" disabled={pending} onclick={() => run(() => open(project!, null, project!.statuses[0], ticket!.id))}><Icon name="plus" /> Add subticket</button></div>
      <div class="view-switch subticket-visibility"><button id="active-subtickets" aria-pressed={!subticketsArchived} disabled={subticketsLoading || pending} onclick={() => run(() => showSubtickets(false))}>Active</button><button id="archived-subtickets" aria-pressed={subticketsArchived} disabled={subticketsLoading || pending} onclick={() => run(() => showSubtickets(true))}>Archived</button></div>
      {#each subtickets as child (child.id)}
        <button class="subticket-row" data-subticket-id={child.id} disabled={pending} onclick={() => run(() => open(project!, child.id))}><span class="mono muted">{project.key}-{child.id}</span><span class="subticket-title">{child.title}</span><span class="subticket-status">{child.status}{child.archived ? " · Archived" : ""}</span></button>
      {:else}<p class="empty-activity">{subticketsLoading ? "Loading subtickets…" : subticketsArchived ? "No archived subtickets." : "No subtickets yet."}</p>{/each}
      <button id="load-subtickets" class="quiet" hidden={!subticketsMore} disabled={subticketsLoading || pending} onclick={() => run(() => loadSubtickets(true))}>More subtickets</button>
      {#if subtickets.length >= TICKETS_VISIBLE_MAX}<p class="empty-activity">Showing the first 1,000 subtickets. The full record remains available through the API.</p>{/if}
      <small>Subtickets also appear on the main board. Status and archiving are independent.</small>
    </section>
  {/if}
  <section id="discussion" hidden={!ticket}>
    <div class="section-heading"><h3>Discussion</h3><span class="muted">Comments are permanent</span></div>
    <div id="comments">
      {#each comments as comment (comment.id)}<div class="comment"><div class="comment-heading"><span>{comment.actor}</span><time datetime={comment.created_at}>{dateLabel(comment.created_at)}</time></div><p class="comment-body">{comment.body}</p></div>
      {:else}<p class="empty-activity">No updates yet. Start the conversation.</p>{/each}
      {#if comments.length >= ACTIVITY_VISIBLE_MAX}<p class="empty-activity">Showing the first 500 entries. The full record remains available through the API.</p>{/if}
    </div>
    <button id="load-comments" class="quiet" hidden={!commentsMore} disabled={commentsLoading || pending} onclick={() => run(() => loadComments(true))}>More comments</button>
    <form id="comment-form" onsubmit={(event) => run(() => postComment(event))}><label class="sr-only" for="comment-body">Comment</label><textarea id="comment-body" bind:value={commentBody} rows="3" required maxlength="16000" aria-describedby="comment-counter comment-error" aria-invalid={commentTooLong} placeholder="Leave an update…" disabled={pending}></textarea><small id="comment-counter" class:over-limit={commentTooLong}>{commentCharacters.toLocaleString()} / {COMMENT_CHARACTERS_MAX.toLocaleString()} characters</small><p id="comment-error" class="form-error" role="alert">{commentError}</p><div class="form-actions"><button id="post-comment" type="submit" class="quiet bordered" disabled={pending || commentTooLong}>Post comment</button></div></form>
    <div class="section-heading"><h3>History</h3><span class="muted">Recorded with every change</span></div>
    <div id="history">
      {#each history as item (item.id)}<div class="history-item"><span>{item.actor} {descriptions[item.kind] ?? item.kind}{item.data.status ? ` · ${item.data.status}` : ""}</span><time datetime={item.created_at}>{dateLabel(item.created_at)}</time></div>
      {:else}<p class="empty-activity">No history yet.</p>{/each}
      {#if history.length >= ACTIVITY_VISIBLE_MAX}<p class="empty-activity">Showing the first 500 entries. The full record remains available through the API.</p>{/if}
    </div>
    <button id="load-history" class="quiet" hidden={!historyMore} disabled={historyLoading || pending} onclick={() => run(() => loadHistory(true))}>More history</button>
  </section>
  </div>
  <footer class="ticket-footer">
    <p id="ticket-error" class="form-error" role="alert">{ticketError}</p>
    <div class="form-actions"><button id="reload-ticket" class="quiet" type="button" hidden={!reload} disabled={pending} onclick={() => { if (project && ticket) run(() => open(project!, ticket!.id)); }}>Reload latest</button><button id="save-ticket" type="submit" form="ticket-form" class="primary" disabled={pending}>{ticket ? "Save changes" : "Create ticket"}</button></div>
  </footer>
</dialog>
