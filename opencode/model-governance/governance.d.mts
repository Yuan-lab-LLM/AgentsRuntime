export class ModelUnavailable extends Error { readonly code: string }
export class GovernanceUnavailable extends Error { readonly code: string }
export interface Task { sessionID: string; taskID: string; revoked: boolean }
export interface Entry { id: string; context_window: number; tools: boolean; reasoning: boolean }
export interface Governance {
  enabled: boolean;
  begin(sessionID: string, messages: readonly { type: string; id: string }[]): Promise<Task | undefined>;
  check(task: Task | undefined): Promise<void>;
  headers(task: Task | undefined): Record<string,string>;
  finish(task: Task | undefined, cancelled?: boolean): Promise<void>;
  finishSession(sessionID: string): Promise<void>;
  flushReceipts(): Promise<void>;
  catalog(): Promise<Entry[] | undefined>;
  projectCatalog<T extends { providerID: string; id: string }>(existing: T[]): Promise<T[]>;
}
export function createGovernance(options?: { env?: Record<string,string | undefined>; fetcher?: typeof fetch; now?: ()=>number }): Governance;
export const governance: Governance;
