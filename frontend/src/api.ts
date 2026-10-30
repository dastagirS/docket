export const PAGE_SIZE = 100;
export const TICKETS_VISIBLE_MAX = 1000;
export const ACTIVITY_VISIBLE_MAX = 500;
export const STATUSES_MAX = 12;
export const STATUS_BYTES_MAX = 100;
export const ASSIGNEE_BYTES_MAX = 100;
export const DEFAULT_STATUSES = ["To Do", "In Progress", "Blocked", "In Review", "Done"];
const REQUEST_TIMEOUT_MS = 10000;

export interface Project {
  id: number;
  name: string;
  key: string;
  statuses: string[];
  version: number;
  created_at: string;
  archived: boolean;
}
export interface Ticket {
  id: number;
  project_id: number;
  title: string;
  description: string;
  status: string;
  version: number;
  created_at: string;
  updated_at: string;
  archived: boolean;
  assignee: string;
  parent_id: number | null;
}
export interface Comment {
  id: number;
  ticket_id: number;
  body: string;
  actor: string;
  created_at: string;
}
export interface ChangeEvent {
  id: number;
  actor: string;
  kind: string;
  created_at: string;
  data: { status?: string };
}
export interface Page<T> { items: T[]; next_cursor: number }

export class ApiError extends Error {
  constructor(message: string, public status: number) { super(message); }
}
export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Operation failed. Reload to check the result.";
}
function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid server response");
  return value as Record<string, unknown>;
}
function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("Invalid server text");
  return value;
}
function integer(value: unknown, minimum = 1): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) throw new Error("Invalid server identifier or version");
  return value;
}
export function readProject(value: unknown): Project {
  const item = record(value);
  if (!Array.isArray(item.statuses) || item.statuses.length < 1 || item.statuses.length > STATUSES_MAX) throw new Error("Invalid project statuses");
  if (typeof item.archived !== "boolean") throw new Error("Invalid project archive state");
  return { id: integer(item.id), name: text(item.name), key: text(item.key), statuses: item.statuses.map(text), version: integer(item.version), created_at: text(item.created_at), archived: item.archived };
}
export function readTicket(value: unknown): Ticket {
  const item = record(value);
  if (typeof item.archived !== "boolean") throw new Error("Invalid ticket archive state");
  return { id: integer(item.id), project_id: integer(item.project_id), title: text(item.title), description: text(item.description), status: text(item.status), version: integer(item.version), created_at: text(item.created_at), updated_at: text(item.updated_at), archived: item.archived, assignee: text(item.assignee), parent_id: item.parent_id === null ? null : integer(item.parent_id) };
}
export function readComment(value: unknown): Comment {
  const item = record(value);
  return { id: integer(item.id), ticket_id: integer(item.ticket_id), body: text(item.body), actor: text(item.actor), created_at: text(item.created_at) };
}
export function readEvent(value: unknown): ChangeEvent {
  const item = record(value);
  const data = record(item.data);
  return { id: integer(item.id), kind: text(item.kind), actor: text(item.actor), created_at: text(item.created_at), data: { status: data.status === undefined ? undefined : text(data.status) } };
}
export function readCommentResult(value: unknown): { comment: Comment; ticket_version: number } {
  const item = record(value);
  return { comment: readComment(item.comment), ticket_version: integer(item.ticket_version) };
}
export function readPage<T>(read: (value: unknown) => T): (value: unknown) => Page<T> {
  return (value) => {
    const page = record(value);
    if (!Array.isArray(page.items) || page.items.length > PAGE_SIZE) throw new Error("Invalid server page");
    return { items: page.items.map(read), next_cursor: integer(page.next_cursor, 0) };
  };
}
export async function api<T>(path: string, options: RequestInit, read: (value: unknown) => T): Promise<T> {
  const response = await fetch(path, { ...options, headers: { "Content-Type": "application/json", ...options.headers }, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS), credentials: "omit" });
  let value: unknown;
  try { value = await response.json(); } catch { throw new Error("Unexpected server response. Reload to check whether your change was saved."); }
  if (!response.ok) {
    const failure = record(value);
    throw new ApiError(typeof failure.error === "string" ? failure.error : "Request failed. Reload before trying again.", response.status);
  }
  return read(value);
}
export function projectPath(projectID: number): string { return `/api/projects/${projectID}`; }
export function ticketPath(projectID: number, ticketID: number): string { return `${projectPath(projectID)}/tickets/${ticketID}`; }
export function dateLabel(value: string): string { return new Date(value).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }); }
