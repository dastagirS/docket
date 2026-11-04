<script lang="ts">
  import { onMount, tick } from "svelte";
  import { on } from "svelte/events";
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

  const DRAG_DISTANCE_PX = 8;
  const DRAG_TICK_MS = 16;
  const DRAG_TIMEOUT_MS = 30000;
  const TOUCH_HOLD_MS = 300;
  const SCROLL_EDGE_PX = 24;
  const SCROLL_STEP_PX = 8;
  type StatusDrag = { sourceIndex: number; insertionIndex: number; active: boolean; inside: boolean; kind: "mouse" | "touch"; identifier: number; startX: number; startY: number; x: number; y: number; scrollStart: number };
  let drag: StatusDrag | null = null;
  let dragOffsetY = 0;
  let dragTick: ReturnType<typeof setInterval> | undefined;
  let dragTimeout: ReturnType<typeof setTimeout> | undefined;
  let touchHold: ReturnType<typeof setTimeout> | undefined;

  function resetDrag(): void {
    clearInterval(dragTick);
    clearTimeout(dragTimeout);
    clearTimeout(touchHold);
    const previous = drag;
    drag = null;
    dragOffsetY = 0;
    if (previous?.kind === "mouse" && statusList?.hasPointerCapture(previous.identifier)) {
      try { statusList.releasePointerCapture(previous.identifier); }
      catch { notify("Could not release the status drag. Release the pointer before trying again."); }
    }
  }
  function updateDrag(): void {
    if (!drag?.active) return;
    const bounds = statusList.getBoundingClientRect();
    drag.inside = drag.x >= bounds.left && drag.x <= bounds.right && drag.y >= bounds.top && drag.y <= bounds.bottom;
    if (!drag.inside) return;
    if (drag.y < bounds.top + SCROLL_EDGE_PX) statusList.scrollTop -= SCROLL_STEP_PX;
    else if (drag.y > bounds.bottom - SCROLL_EDGE_PX) statusList.scrollTop += SCROLL_STEP_PX;
    const source = statusList.children[drag.sourceIndex] as HTMLElement;
    const last = statusList.children[statuses.length - 1] as HTMLElement;
    // Transforms can expand scrollHeight; clamp the preview so edge scrolling cannot grow its own range.
    dragOffsetY = Math.max(-source.offsetTop, Math.min(drag.y - drag.startY + statusList.scrollTop - drag.scrollStart, last.offsetTop + last.offsetHeight - source.offsetHeight - source.offsetTop));
    let insertion = statuses.length;
    for (let i = 0; i < statuses.length; i++) {
      const row = statusList.children[i] as HTMLElement;
      const middle = bounds.top + statusList.clientTop + row.offsetTop - statusList.scrollTop + row.offsetHeight / 2;
      if (drag.y < middle) { insertion = i; break; }
    }
    drag.insertionIndex = insertion;
  }
  function activateDrag(): void {
    if (!drag || pending || !expanded || !dialog.open || drag.sourceIndex >= statuses.length) { resetDrag(); return; }
    if (drag.kind === "mouse") {
      try { statusList.setPointerCapture(drag.identifier); }
      catch { resetDrag(); notify("Could not start the status drag. Try again."); return; }
    }
    drag.active = true;
    updateDrag();
    dragTick = setInterval(updateDrag, DRAG_TICK_MS);
  }
  function prepareDrag(index: number, kind: "mouse" | "touch", identifier: number, x: number, y: number): void {
    drag = { sourceIndex: index, insertionIndex: index, active: false, inside: true, kind, identifier, startX: x, startY: y, x, y, scrollStart: statusList.scrollTop };
    dragTimeout = setTimeout(resetDrag, DRAG_TIMEOUT_MS);
  }
  function pointerStarted(event: PointerEvent, index: number): void {
    if (pending || drag || !event.isPrimary || event.button !== 0 || event.pointerType === "touch" || (event.target as HTMLElement).closest("button")) return;
    prepareDrag(index, "mouse", event.pointerId, event.clientX, event.clientY);
  }
  function pointerMoved(event: PointerEvent): void {
    if (!drag || drag.kind !== "mouse" || drag.identifier !== event.pointerId) return;
    if (event.buttons !== 1) { resetDrag(); return; }
    drag.x = event.clientX;
    drag.y = event.clientY;
    if (!drag.active && Math.hypot(drag.x - drag.startX, drag.y - drag.startY) >= DRAG_DISTANCE_PX) activateDrag();
    if (drag?.active) event.preventDefault();
  }
  function finishDrag(x: number, y: number): void {
    if (!drag) return;
    drag.x = x;
    drag.y = y;
    updateDrag();
    const current = drag;
    const target = current.insertionIndex > current.sourceIndex ? current.insertionIndex - 1 : current.insertionIndex;
    resetDrag();
    if (current.active && current.inside) run(() => moveStatus(current.sourceIndex, target, current.kind !== "touch"));
  }
  function pointerEnded(event: PointerEvent): void {
    if (drag?.kind === "mouse" && drag.identifier === event.pointerId) finishDrag(event.clientX, event.clientY);
  }
  function touchStarted(event: TouchEvent, index: number): void {
    if (pending || drag || event.touches.length !== 1 || (event.target as HTMLElement).closest("button")) return;
    const touch = event.touches[0];
    prepareDrag(index, "touch", touch.identifier, touch.clientX, touch.clientY);
    touchHold = setTimeout(activateDrag, TOUCH_HOLD_MS);
  }
  function touchMoved(event: TouchEvent): void {
    if (drag?.kind !== "touch") return;
    const touch = event.touches[0];
    if (event.touches.length !== 1 || touch.identifier !== drag.identifier) { resetDrag(); return; }
    drag.x = touch.clientX;
    drag.y = touch.clientY;
    if (drag.active) {
      if (!event.cancelable) { resetDrag(); return; }
      event.preventDefault();
    } else if (Math.hypot(drag.x - drag.startX, drag.y - drag.startY) >= DRAG_DISTANCE_PX) resetDrag();
  }
  function touchEnded(event: TouchEvent): void {
    if (drag?.kind !== "touch") return;
    const touch = event.changedTouches[0];
    if (!touch || touch.identifier !== drag.identifier) { resetDrag(); return; }
    if (drag.active) event.preventDefault();
    finishDrag(touch.clientX, touch.clientY);
  }
  function dragKeyPressed(event: KeyboardEvent): void {
    if (event.key === "Escape" && drag) { event.preventDefault(); resetDrag(); }
  }
  function statusKeyPressed(event: KeyboardEvent, index: number): void {
    if (pending || drag || !event.altKey || event.ctrlKey || event.metaKey || event.isComposing) return;
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    run(() => moveStatus(index, index + (event.key === "ArrowUp" ? -1 : 1)));
  }
  onMount(() => {
    // Svelte makes touchmove passive by default; active sorting must stop native scrolling.
    const removeMove = on(window, "touchmove", touchMoved, { passive: false });
    const removeEnd = on(window, "touchend", touchEnded, { passive: false });
    return () => { removeMove(); removeEnd(); resetDrag(); };
  });

  export function open(project: Project | null): void {
    if (pending) return;
    resetDrag();
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
  function close(): void { if (!pending) { resetDrag(); dialog.close(); } }
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
  async function moveStatus(sourceIndex: number, targetIndex: number, focus = true): Promise<void> {
    if (pending || sourceIndex < 0 || sourceIndex >= statuses.length || targetIndex < 0 || targetIndex >= statuses.length || sourceIndex === targetIndex) return;
    const reordered = [...statuses];
    const [status] = reordered.splice(sourceIndex, 1);
    reordered.splice(targetIndex, 0, status);
    statuses = reordered;
    statusChanged();
    if (focus) await focusStatus(targetIndex);
  }
  async function removeStatus(index: number): Promise<void> {
    if (pending || drag?.active || statuses.length <= 1) return;
    resetDrag();
    statuses = statuses.filter((_, itemIndex) => itemIndex !== index);
    statusChanged();
    await focusStatus(Math.min(index, statuses.length - 1));
  }
  async function addStatus(): Promise<void> {
    if (pending || drag?.active || statuses.length >= STATUSES_MAX) return;
    resetDrag();
    statuses = [...statuses, ""];
    statusChanged();
    await focusStatus(statuses.length - 1);
  }
  function run(action: () => Promise<void>): void { action().catch((failure: unknown) => notify(errorMessage(failure))); }
  async function toggleArchive(): Promise<void> {
    if (!editing || pending || drag?.active) return;
    resetDrag();
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
    if (pending || drag?.active) return;
    resetDrag();
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

<svelte:window onpointermove={pointerMoved} onpointerup={pointerEnded} onpointercancel={resetDrag} ontouchcancel={resetDrag} onblur={resetDrag} onkeydown={dragKeyPressed} />

<dialog id="project-dialog" class="project-dialog" class:settings-dialog={!!editing} aria-labelledby="project-dialog-title" bind:this={dialog} oncancel={(event) => { if (pending || drag) { event.preventDefault(); resetDrag(); } }}>
  <form id="project-form" onsubmit={(event) => run(() => save(event))}>
    <div class="project-form-header">
      <div class="dialog-heading"><div class="project-heading">{#if editing}<span class="settings-mark"><Icon name="settings" /></span>{/if}<div>{#if editing}<span class="settings-eyebrow">{editing.key} / PROJECT</span>{/if}<h2 id="project-dialog-title">{editing ? "Project settings" : "New project"}</h2></div></div><button type="button" id="close-project" class="icon-button" aria-label="Close project form" disabled={pending || !!drag?.active} onclick={close}><Icon name="close" /></button></div>
      <p class="muted" id="project-help">{editing ? "Manage the details, workflow, and visibility of your project." : "Give this work a place to live."}</p>
    </div>
    <div class="project-form-body">
      <section class="project-general" aria-label="Project details">
        {#if editing}<h3>General</h3><p class="settings-section-help">A clear name makes this project easy to find.</p>{/if}
        <div class="project-fields">
          <label>Project name<input id="project-title" bind:this={titleInput} bind:value={name} oninput={nameChanged} required maxlength="100" placeholder="e.g. Personal website" autocomplete="off" disabled={pending || !!drag?.active}></label>
          <label>Project key<input id="project-key" bind:value={key} oninput={() => { keyManual = true; }} required minlength="2" maxlength="10" pattern={"[A-Za-z0-9]{2,10}"} placeholder="WEB" autocomplete="off" disabled={pending || !!drag?.active || !!editing}><small>{editing ? "Permanent identifier. Cannot be changed." : "A short, unique label. Letters and numbers only."}</small></label>
        </div>
      </section>
      <section class="workflow" aria-labelledby="workflow-label">
        <div class="workflow-heading"><div class="workflow-title"><h3 id="workflow-label">Statuses</h3><span id="status-count" aria-label={`${statuses.length} of ${STATUSES_MAX} statuses`}>{statuses.length} / {STATUSES_MAX}</span></div><button id="workflow-toggle" class="workflow-toggle" type="button" aria-expanded={expanded} aria-controls="workflow-editor" disabled={pending || !!drag?.active} onclick={() => { resetDrag(); expanded = !expanded; }}><Icon name={expanded ? "arrow-up" : "settings"} />{expanded ? "Collapse" : "Edit statuses"}</button></div>
        <div id="workflow-preview" class="status-preview" aria-label="Project statuses" hidden={expanded}>
          {#each statuses as status, index}<span class="status-chip"><span class={`status-dot tone-${index % 5}`}></span><span class="status-chip-name">{status}</span></span>{/each}
        </div>
        <div id="workflow-editor" hidden={!expanded}>
          <ol id="workflow-statuses" class="status-list" class:status-sorting={!!drag?.active} aria-label="Workflow statuses" bind:this={statusList}>
            {#each statuses as status, index}
              <li class="status-row" class:status-invalid={invalidIndex === index} class:status-dragging={drag?.active && drag.sourceIndex === index} class:status-insert-before={drag?.active && drag.inside && drag.insertionIndex === index && drag.sourceIndex !== index} class:status-insert-after={drag?.active && drag.inside && drag.insertionIndex === statuses.length && index === statuses.length - 1 && drag.sourceIndex !== index} style:transform={drag?.active && drag.sourceIndex === index ? `translateY(${dragOffsetY}px)` : undefined} onpointerdown={(event) => pointerStarted(event, index)} ontouchstart={(event) => touchStarted(event, index)} ondragstart={(event) => event.preventDefault()} oncontextmenu={(event) => { if (drag?.active) event.preventDefault(); }}>
                <span class="status-order" aria-hidden="true">{String(index + 1).padStart(2, "0")}</span>
                <div class="status-title-line"><span class={`status-dot tone-${index % 5}`} aria-hidden="true"></span><input class="status-name" type="text" bind:value={statuses[index]} oninput={statusChanged} onkeydown={(event) => statusKeyPressed(event, index)} maxlength={STATUS_BYTES_MAX} placeholder="Status name" autocomplete="off" aria-label={`Status ${index + 1} name`} aria-required="true" aria-invalid={invalidIndex === index ? "true" : undefined} aria-describedby={`status-error workflow-reorder-help${index === 0 ? " workflow-default-help" : ""}`} disabled={pending || !!drag?.active}>{#if index === 0}<span class="status-default">Default</span>{/if}</div>
                <div class="status-controls">
                  <button type="button" class="status-control status-remove" aria-label={`Remove status ${index + 1}`} title="Remove status" disabled={pending || !!drag?.active || statuses.length <= 1} onclick={() => run(() => removeStatus(index))}><Icon name="close" /></button>
                </div>
              </li>
            {/each}
          </ol>
          <div class="workflow-actions"><button id="add-status" class="add-status" type="button" disabled={pending || !!drag?.active || statuses.length >= STATUSES_MAX} onclick={() => run(addStatus)}><Icon name="plus" />{statuses.length >= STATUSES_MAX ? "Status limit reached" : "Add a status"}</button></div>
          <p id="workflow-reorder-help" class="workflow-hint">Drag rows to reorder. Hold on touch. Keyboard: Alt + Up / Down.</p>
          <p id="workflow-default-help" class="workflow-hint">New tickets start in the first status. Board columns follow this order.</p>
          <p class="workflow-hint workflow-safety">Move tickets before removing or renaming a status in use.</p>
        </div>
        <p id="status-error" class="form-error" role="alert">{statusError}</p>
      </section>
      {#if editing}
        <section id="project-lifecycle" class="project-lifecycle" class:project-restoring={editing.archived} aria-labelledby="project-lifecycle-title">
          <div><h3 id="project-lifecycle-title">{editing.archived ? "Bring this project back" : "Delete project"}</h3><p id="project-lifecycle-help">{editing.archived ? "Restore this project to Active. Its tickets, comments, and history stay exactly as they are." : "Deleting moves this project to Archived. Tickets, comments, and history are kept. You can restore it anytime."}</p></div>
          <button id="archive-project" type="button" class={editing.archived ? "quiet bordered" : "danger-button"} aria-describedby="project-lifecycle-help" disabled={pending || !!drag?.active} onclick={() => run(toggleArchive)}><Icon name={editing.archived ? "restore" : "archive"} />{editing.archived ? "Restore project" : "Delete project"}</button>
        </section>
      {/if}
    </div>
    <div class="project-form-footer"><p id="project-error" class="form-error" role="alert">{error}</p><div class="form-actions"><button type="button" id="cancel-project" class="quiet" disabled={pending || !!drag?.active} onclick={close}>Cancel</button><button type="submit" id="save-project" class="primary" disabled={pending || !!drag?.active}>{editing ? "Save changes" : "Create project"}</button></div></div>
  </form>
</dialog>
