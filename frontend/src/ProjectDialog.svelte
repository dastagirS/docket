<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import { api, ApiError, errorMessage, DEFAULT_STATUSES, STATUSES_MAX, STATUS_BYTES_MAX, projectPath, readProject, type Project } from "./api";

  export let onsaved: (project: Project) => Promise<void>;
  export let onarchived: (project: Project) => Promise<void>;
  export let notify: (message: string) => void;
  let dialog: HTMLDialogElement;
  let titleInput: HTMLInputElement;
  let statusList: HTMLOListElement;
  let editing: Project | null = null;
  let name = "";
  let key = "";
  let keyManual = false;
  let statuses = [...DEFAULT_STATUSES];
  let expanded = false;
  let pending = false;
  let error = "";
  let statusError = "";
  let invalidIndex = -1;

  export function open(project: Project | null): void {
    if (pending) return;
    editing = project ? { ...project, statuses: [...project.statuses] } : null;
    name = project?.name ?? "";
    key = project?.key ?? "";
    keyManual = false;
    statuses = [...(project?.statuses ?? DEFAULT_STATUSES)];
    expanded = !!project;
    error = "";
    statusError = "";
    invalidIndex = -1;
    dialog.showModal();
    titleInput.focus();
  }
  function close(): void { if (!pending) dialog.close(); }
  function nameChanged(event: Event & { currentTarget: HTMLInputElement }): void {
    if (!editing && !keyManual) key = event.currentTarget.value.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 5);
  }
  async function focusStatus(index: number): Promise<void> {
    await tick();
    const input = statusList.children[index]?.querySelector("input");
    input?.focus({ preventScroll: true });
    input?.scrollIntoView({ block: "nearest" });
  }
  function statusChanged(): void { statusError = ""; invalidIndex = -1; }
  async function moveStatus(index: number, offset: number): Promise<void> {
    const target = index + offset;
    if (pending || target < 0 || target >= statuses.length) return;
    [statuses[index], statuses[target]] = [statuses[target], statuses[index]];
    statuses = [...statuses];
    statusChanged();
    await focusStatus(target);
  }
  async function removeStatus(index: number): Promise<void> {
    if (pending || statuses.length <= 1) return;
    statuses = statuses.filter((_, itemIndex) => itemIndex !== index);
    statusChanged();
    await focusStatus(Math.min(index, statuses.length - 1));
  }
  async function addStatus(): Promise<void> {
    if (pending || statuses.length >= STATUSES_MAX) return;
    statuses = [...statuses, ""];
    statusChanged();
    await focusStatus(statuses.length - 1);
  }
  function run(action: () => Promise<void>): void { action().catch((failure: unknown) => notify(errorMessage(failure))); }
  async function toggleArchive(): Promise<void> {
    if (!editing || pending) return;
    const selected = editing;
    const unsaved = name !== selected.name || statuses.length !== selected.statuses.length || statuses.some((status, index) => status !== selected.statuses[index]);
    const message = selected.archived
      ? `Restore ${selected.name} to active projects?`
      : `Delete ${selected.name}? It will be archived, not permanently deleted. Tickets, comments, and history are kept, and you can restore it later.`;
    if ((!selected.archived || unsaved) && !confirm(message + (unsaved ? " Unsaved settings will be discarded." : ""))) return;
    error = "";
    pending = true;
    try {
      const saved = await api(projectPath(selected.id), { method: "PATCH", headers: { "If-Match": `"${selected.version}"` }, body: JSON.stringify({ archived: !selected.archived }) }, readProject);
      dialog.close();
      await onarchived(saved);
      notify(saved.archived ? "Project moved to Archived. Nothing was permanently deleted." : "Project restored.");
    } catch (failure) {
      error = errorMessage(failure) + (failure instanceof ApiError ? "" : " Reload to check the result before submitting again.");
      if (!dialog.open) notify(error);
    } finally { pending = false; }
  }
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending) return;
    const workflow = statuses.map((status) => status.trim());
    for (const [index, status] of workflow.entries()) {
      let message = "";
      if (!status) message = "Give every status a name.";
      else if (new TextEncoder().encode(status).length > STATUS_BYTES_MAX) message = "Status names must fit within 100 UTF-8 bytes.";
      else if (workflow.indexOf(status) !== index) message = "Status names must be unique.";
      if (message) {
        expanded = true;
        invalidIndex = index;
        statusError = message;
        await focusStatus(index);
        return;
      }
    }
    statusChanged();
    error = "";
    pending = true;
    try {
      const input = { name: name.trim(), statuses: workflow, ...(!editing ? { key: key.toUpperCase() } : {}) };
      const saved = await api(editing ? projectPath(editing.id) : "/api/projects", { method: editing ? "PATCH" : "POST", body: JSON.stringify(input), headers: editing ? { "If-Match": `"${editing.version}"` } : {} }, readProject);
      dialog.close();
      await onsaved(saved);
    } catch (failure) {
      error = errorMessage(failure) + (failure instanceof ApiError ? "" : " Reload to check the result before submitting again.");
      if (!dialog.open) notify(error);
    } finally { pending = false; }
  }
</script>

<dialog id="project-dialog" class="project-dialog" class:settings-dialog={!!editing} aria-labelledby="project-dialog-title" bind:this={dialog} oncancel={(event) => { if (pending) event.preventDefault(); }}>
  <form id="project-form" onsubmit={(event) => run(() => save(event))}>
    <div class="project-form-header">
      <div class="dialog-heading"><div class="project-heading">{#if editing}<span class="settings-mark"><Icon name="settings" /></span>{/if}<div>{#if editing}<span class="settings-eyebrow">{editing.key} / PROJECT</span>{/if}<h2 id="project-dialog-title">{editing ? "Project settings" : "New project"}</h2></div></div><button type="button" id="close-project" class="icon-button" aria-label="Close project form" disabled={pending} onclick={close}><Icon name="close" /></button></div>
      <p class="muted" id="project-help">{editing ? "Manage the details, workflow, and visibility of your project." : "Give this work a place to live."}</p>
    </div>
    <div class="project-form-body">
      <section class="project-general" aria-label="Project details">
        {#if editing}<h3>General</h3><p class="settings-section-help">A clear name makes this project easy to find.</p>{/if}
        <div class="project-fields">
          <label>Project name<input id="project-title" bind:this={titleInput} bind:value={name} oninput={nameChanged} required maxlength="100" placeholder="e.g. Personal website" autocomplete="off" disabled={pending}></label>
          <label>Project key<input id="project-key" bind:value={key} oninput={() => { keyManual = true; }} required minlength="2" maxlength="10" pattern={"[A-Za-z0-9]{2,10}"} placeholder="WEB" autocomplete="off" disabled={pending || !!editing}><small>{editing ? "Permanent identifier. Cannot be changed." : "A short, unique label. Letters and numbers only."}</small></label>
        </div>
      </section>
      <section class="workflow" aria-labelledby="workflow-label">
        <div class="workflow-heading"><div class="workflow-title"><h3 id="workflow-label">Statuses</h3><span id="status-count" aria-label={`${statuses.length} of ${STATUSES_MAX} statuses`}>{statuses.length} / {STATUSES_MAX}</span></div><button id="workflow-toggle" class="workflow-toggle" type="button" aria-expanded={expanded} aria-controls="workflow-editor" disabled={pending} onclick={() => { expanded = !expanded; }}><Icon name={expanded ? "arrow-up" : "settings"} />{expanded ? "Collapse" : "Edit statuses"}</button></div>
        <div id="workflow-preview" class="status-preview" aria-label="Project statuses" hidden={expanded}>
          {#each statuses as status, index}<span class="status-chip"><span class={`status-dot tone-${index % 5}`}></span><span class="status-chip-name">{status}</span></span>{/each}
        </div>
        <div id="workflow-editor" hidden={!expanded}>
          <ol id="workflow-statuses" class="status-list" aria-label="Workflow statuses" bind:this={statusList}>
            {#each statuses as status, index}
              <li class="status-row" class:status-invalid={invalidIndex === index}>
                <span class="status-order" aria-hidden="true">{String(index + 1).padStart(2, "0")}</span>
                <div class="status-title-line"><span class={`status-dot tone-${index % 5}`} aria-hidden="true"></span><input class="status-name" type="text" bind:value={statuses[index]} oninput={statusChanged} maxlength={STATUS_BYTES_MAX} placeholder="Status name" autocomplete="off" aria-label={`Status ${index + 1} name`} aria-required="true" aria-invalid={invalidIndex === index ? "true" : undefined} aria-describedby={index === 0 ? "status-error workflow-default-help" : "status-error"} disabled={pending}>{#if index === 0}<span class="status-default">Default</span>{/if}</div>
                <div class="status-controls">
                  <div class="status-reorder">
                    <button type="button" class="status-control status-up" aria-label={`Move status ${index + 1} up`} title="Move up" disabled={pending || index === 0} onclick={() => run(() => moveStatus(index, -1))}><Icon name="arrow-up" /></button>
                    <button type="button" class="status-control status-down" aria-label={`Move status ${index + 1} down`} title="Move down" disabled={pending || index === statuses.length - 1} onclick={() => run(() => moveStatus(index, 1))}><Icon name="arrow-down" /></button>
                  </div>
                  <button type="button" class="status-control status-remove" aria-label={`Remove status ${index + 1}`} title="Remove status" disabled={pending || statuses.length <= 1} onclick={() => run(() => removeStatus(index))}><Icon name="close" /></button>
                </div>
              </li>
            {/each}
          </ol>
          <div class="workflow-actions"><button id="add-status" class="add-status" type="button" disabled={pending || statuses.length >= STATUSES_MAX} onclick={() => run(addStatus)}><Icon name="plus" />{statuses.length >= STATUSES_MAX ? "Status limit reached" : "Add a status"}</button></div>
          <p id="workflow-default-help" class="workflow-hint">New tickets start in the first status. Board columns follow this order.</p>
          <p class="workflow-hint workflow-safety">Move tickets before removing or renaming a status in use.</p>
        </div>
        <p id="status-error" class="form-error" role="alert">{statusError}</p>
      </section>
      {#if editing}
        <section id="project-lifecycle" class="project-lifecycle" class:project-restoring={editing.archived} aria-labelledby="project-lifecycle-title">
          <div><h3 id="project-lifecycle-title">{editing.archived ? "Bring this project back" : "Delete project"}</h3><p id="project-lifecycle-help">{editing.archived ? "Restore this project to Active. Its tickets, comments, and history stay exactly as they are." : "Deleting moves this project to Archived. Tickets, comments, and history are kept. You can restore it anytime."}</p></div>
          <button id="archive-project" type="button" class={editing.archived ? "quiet bordered" : "danger-button"} aria-describedby="project-lifecycle-help" disabled={pending} onclick={() => run(toggleArchive)}><Icon name={editing.archived ? "restore" : "archive"} />{editing.archived ? "Restore project" : "Delete project"}</button>
        </section>
      {/if}
    </div>
    <div class="project-form-footer"><p id="project-error" class="form-error" role="alert">{error}</p><div class="form-actions"><button type="button" id="cancel-project" class="quiet" disabled={pending} onclick={close}>Cancel</button><button type="submit" id="save-project" class="primary" disabled={pending}>{editing ? "Save changes" : "Create project"}</button></div></div>
  </form>
</dialog>
