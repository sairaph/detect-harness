import { spawn } from "node:child_process";

export const PROTOCOL_VERSION = 1 as const;
export const DEFAULT_MAX_OUTPUT_BYTES = 32 * 1024 * 1024;
const TERMINATION_GRACE_MS = 250;

export type HarnessId =
  | "claude-desktop"
  | "claude-code"
  | "cursor"
  | "codex"
  | "gemini-cli"
  | "windsurf"
  | "zed"
  | "cline"
  | "roo-code"
  | "zoo-code"
  | "amazon-q"
  | "continue"
  | "opencode"
  | "vscode";

export type DesiredState = "present" | "absent";
export type ConflictPolicy = "error" | "replace";
export type DetectionState = "present" | "absent" | "unavailable";
export type ChangeState = "ready" | "noop" | "conflict" | "unavailable";
export type ResultState = "applied" | "noop" | "skipped" | "conflict" | "failed";
export type ChangeAction = "add" | "update" | "remove";
export type ScopeMode = "project";

const HARNESS_IDS = [
  "claude-desktop",
  "claude-code",
  "cursor",
  "codex",
  "gemini-cli",
  "windsurf",
  "zed",
  "cline",
  "roo-code",
  "zoo-code",
  "amazon-q",
  "continue",
  "opencode",
  "vscode",
] as const satisfies readonly HarnessId[];

export interface StdioServer {
  name: string;
  command: string;
  args?: readonly string[];
  env?: Record<string, string>;
}

/** Selects directory-local (project) configuration for harnesses that support it. */
export interface Scope {
  mode: ScopeMode;
  dir: string;
}

/** Build a project scope targeting dir. Throws if dir is empty. */
export function projectScope(dir: string): Scope {
  if (typeof dir !== "string" || dir.trim() === "") {
    throw new TypeError("project scope requires a directory");
  }
  return { mode: "project", dir: dir.trim() };
}

/** Project-scoped configuration support metadata for a harness. Informational. */
export interface ProjectScope {
  path: string;
  reloadHint: string;
  lifecycle: string;
  shareable: boolean;
  trustGate: boolean;
}

export interface Detection {
  id: HarnessId;
  name: string;
  reloadHint: string;
  state: DetectionState;
  evidence?: string[];
  reason?: string;
  configPath?: string;
  configError?: string;
  scope?: ScopeMode;
  scopeDir?: string;
  project?: ProjectScope;
}

export interface Change {
  harnessId: HarnessId;
  name: string;
  path?: string;
  desired: DesiredState;
  state: ChangeState;
  action?: ChangeAction;
  reason?: string;
  scope?: ScopeMode;
  scopeDir?: string;
}

export interface UpdateResult {
  harnessId: HarnessId;
  name: string;
  path?: string;
  desired: DesiredState;
  state: ResultState;
  action?: ChangeAction;
  reason?: string;
  scope?: ScopeMode;
  scopeDir?: string;
}

export interface UpdateOutcome {
  changes: Change[];
  results: UpdateResult[];
}

export interface ClientOptions {
  /** Overrides DETECT_HARNESS_BIN and PATH lookup. */
  binaryPath?: string;
  /** Combined stdout and stderr limit. Defaults to 32 MiB. */
  maxOutputBytes?: number;
  /** Optional process timeout. No timeout is applied when omitted. */
  timeoutMs?: number;
}

export interface UpdateOptions {
  conflictPolicy?: ConflictPolicy;
  scope?: Scope;
}

interface Request {
  version: typeof PROTOCOL_VERSION;
  operation: "detect" | "render" | "update";
  server?: StdioServer;
  harness?: HarnessId;
  harnesses?: readonly HarnessId[];
  desired?: DesiredState;
  conflictPolicy?: ConflictPolicy;
  scope?: Scope;
  dryRun?: boolean;
}

interface ResponseEnvelope {
  version: typeof PROTOCOL_VERSION;
  ok: true;
  detections?: unknown;
  config?: unknown;
  changes?: unknown;
  results?: unknown;
}

export class DetectHarnessError extends Error {
  override readonly name: string = "DetectHarnessError";
}

export class InvocationError extends DetectHarnessError {
  override readonly name: string = "InvocationError";
  readonly exitCode: number | null;
  readonly signal: string | null;
  readonly stderr: string;

  constructor(
    message: string,
    options: {
      exitCode?: number | null;
      signal?: string | null;
      stderr?: string;
      cause?: unknown;
    } = {},
  ) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.exitCode = options.exitCode ?? null;
    this.signal = options.signal ?? null;
    this.stderr = options.stderr ?? "";
  }
}

export class OutputLimitError extends InvocationError {
  override readonly name: string = "OutputLimitError";
}

export class ProtocolValidationError extends InvocationError {
  override readonly name: string = "ProtocolValidationError";
}

export class ProtocolError extends InvocationError {
  override readonly name: string = "ProtocolError";
  readonly code: string;

  constructor(
    code: string,
    message: string,
    options: { exitCode?: number | null; signal?: string | null; stderr?: string } = {},
  ) {
    super(`${code}: ${message}`, options);
    this.code = code;
  }
}

export class DetectHarnessClient {
  readonly #binaryPath: string | undefined;
  readonly #maxOutputBytes: number;
  readonly #timeoutMs: number | undefined;

  constructor(options: ClientOptions = {}) {
    if (options.binaryPath !== undefined && options.binaryPath.length === 0) {
      throw new TypeError("binaryPath cannot be empty");
    }
    const maximum = options.maxOutputBytes ?? DEFAULT_MAX_OUTPUT_BYTES;
    if (!Number.isSafeInteger(maximum) || maximum <= 0) {
      throw new TypeError("maxOutputBytes must be a positive integer");
    }
    if (options.timeoutMs !== undefined && (!Number.isFinite(options.timeoutMs) || options.timeoutMs <= 0)) {
      throw new TypeError("timeoutMs must be a positive number");
    }
    this.#binaryPath = options.binaryPath;
    this.#maxOutputBytes = maximum;
    this.#timeoutMs = options.timeoutMs;
  }

  async detect(scope?: Scope): Promise<Detection[]> {
    const request: Request = { version: PROTOCOL_VERSION, operation: "detect" };
    applyScope(request, scope);
    const response = await this.#invoke(request);
    return parseDetections(response.detections);
  }

  async render(harness: HarnessId, server: StdioServer, scope?: Scope): Promise<string> {
    const request: Request = {
      version: PROTOCOL_VERSION,
      operation: "render",
      harness,
      server,
    };
    applyScope(request, scope);
    const response = await this.#invoke(request);
    return expectString(response.config, "response.config");
  }

  async plan(
    harnesses: readonly HarnessId[],
    desired: DesiredState,
    server: StdioServer,
    options: UpdateOptions = {},
  ): Promise<Change[]> {
    const request: Request = {
      version: PROTOCOL_VERSION,
      operation: "update",
      harnesses,
      desired,
      server,
      conflictPolicy: options.conflictPolicy ?? "error",
      dryRun: true,
    };
    applyScope(request, options.scope);
    const response = await this.#invoke(request);
    return parseChanges(response.changes);
  }

  async update(
    harnesses: readonly HarnessId[],
    desired: DesiredState,
    server: StdioServer,
    options: UpdateOptions = {},
  ): Promise<UpdateOutcome> {
    const request: Request = {
      version: PROTOCOL_VERSION,
      operation: "update",
      harnesses,
      desired,
      server,
      conflictPolicy: options.conflictPolicy ?? "error",
    };
    applyScope(request, options.scope);
    const response = await this.#invoke(request);
    return {
      changes: parseChanges(response.changes),
      results: parseResults(response.results),
    };
  }

  async #invoke(request: Request): Promise<ResponseEnvelope> {
    const environmentBinary = process.env.DETECT_HARNESS_BIN;
    const binary = this.#binaryPath ?? (environmentBinary ? environmentBinary : "detect-harness");
    const payload = JSON.stringify(request);

    return await new Promise<ResponseEnvelope>((resolve, reject) => {
      const child = spawn(binary, [], {
        detached: process.platform !== "win32",
        shell: false,
        stdio: ["pipe", "pipe", "pipe"],
        windowsHide: true,
      });
      const stdout: Buffer[] = [];
      const stderr: Buffer[] = [];
      let collectedBytes = 0;
      let outputExceeded = false;
      let timedOut = false;
      let spawnError: Error | undefined;
      let terminating = false;
      let escalationTimer: NodeJS.Timeout | undefined;

      const signalProcessTree = (signal: NodeJS.Signals): void => {
        if (child.pid === undefined) return;
        try {
          if (process.platform === "win32") child.kill(signal);
          else process.kill(-child.pid, signal);
        } catch {
          // The process or group may have exited between observation and signaling.
        }
      };

      const terminate = (): void => {
        if (terminating) return;
        terminating = true;
        signalProcessTree("SIGTERM");
        escalationTimer = setTimeout(() => signalProcessTree("SIGKILL"), TERMINATION_GRACE_MS);
        escalationTimer.unref();
      };

      const collect = (destination: Buffer[]) => (chunk: Buffer): void => {
        if (outputExceeded) return;
        collectedBytes += chunk.length;
        if (collectedBytes > this.#maxOutputBytes) {
          outputExceeded = true;
          terminate();
          return;
        }
        destination.push(chunk);
      };

      child.stdout.on("data", collect(stdout));
      child.stderr.on("data", collect(stderr));
      child.on("error", (error) => {
        spawnError = error;
      });
      child.stdin.on("error", () => {
        // A process may close stdin after emitting a valid structured error.
      });

      const timer = this.#timeoutMs === undefined
        ? undefined
        : setTimeout(() => {
            timedOut = true;
            terminate();
          }, this.#timeoutMs);

      child.on("close", (exitCode, signal) => {
        if (timer !== undefined) clearTimeout(timer);
        if (escalationTimer !== undefined) clearTimeout(escalationTimer);
        const stderrText = Buffer.concat(stderr).toString("utf8");
        const details = { exitCode, signal, stderr: stderrText };

        if (outputExceeded) {
          reject(new OutputLimitError(`companion output exceeded ${this.#maxOutputBytes} bytes`, details));
          return;
        }
        if (timedOut) {
          reject(new InvocationError(`companion timed out after ${this.#timeoutMs} ms`, details));
          return;
        }
        if (spawnError !== undefined && stdout.length === 0) {
          reject(new InvocationError(`could not execute ${JSON.stringify(binary)}: ${spawnError.message}`, {
            ...details,
            cause: spawnError,
          }));
          return;
        }

        let decoded: unknown;
        const stdoutText = Buffer.concat(stdout).toString("utf8");
        try {
          decoded = JSON.parse(stdoutText) as unknown;
        } catch (error) {
          reject(new ProtocolValidationError(
            `companion returned invalid JSON${diagnosticSuffix(stderrText)}`,
            { ...details, cause: error },
          ));
          return;
        }

        try {
          const response = parseEnvelope(decoded, details);
          if (exitCode !== 0) {
            throw new InvocationError(
              `companion returned a successful response but exited with status ${exitCode ?? signal ?? "unknown"}${diagnosticSuffix(stderrText)}`,
              details,
            );
          }
          resolve(response);
        } catch (error) {
          reject(error);
        }
      });

      child.stdin.end(payload);
    });
  }
}

function parseEnvelope(
  value: unknown,
  details: { exitCode: number | null; signal: string | null; stderr: string },
): ResponseEnvelope {
  const response = expectRecord(value, "response");
  if (response.version !== PROTOCOL_VERSION) {
    throw new ProtocolValidationError(
      `unsupported protocol version ${JSON.stringify(response.version)}; expected ${PROTOCOL_VERSION}`,
      details,
    );
  }
  if (typeof response.ok !== "boolean") {
    throw new ProtocolValidationError("response.ok must be a boolean", details);
  }
  if (!response.ok) {
    const error = expectRecord(response.error, "response.error");
    throw new ProtocolError(
      expectString(error.code, "response.error.code"),
      expectString(error.message, "response.error.message"),
      details,
    );
  }
  return response as unknown as ResponseEnvelope;
}

function parseDetections(value: unknown): Detection[] {
  return expectArray(value, "response.detections").map((entry, index) => {
    const path = `response.detections[${index}]`;
    const item = expectRecord(entry, path);
    expectEnum(item.id, HARNESS_IDS, `${path}.id`);
    expectString(item.name, `${path}.name`);
    expectString(item.reloadHint, `${path}.reloadHint`);
    expectEnum(item.state, ["present", "absent", "unavailable"] as const, `${path}.state`);
    expectOptionalStringArray(item.evidence, `${path}.evidence`);
    expectOptionalStrings(item, ["reason", "configPath", "configError", "scopeDir"], path);
    expectOptionalEnum(item, "scope", ["project"] as const, path);
    expectOptionalProject(item, path);
    return item as unknown as Detection;
  });
}

function parseChanges(value: unknown): Change[] {
  return expectArray(value, "response.changes").map((entry, index) => {
    const path = `response.changes[${index}]`;
    const item = expectRecord(entry, path);
    const harnessId = expectEnum(item.harnessId, HARNESS_IDS, `${path}.harnessId`);
    const name = expectString(item.name, `${path}.name`);
    const desired = expectEnum(item.desired, ["present", "absent"] as const, `${path}.desired`);
    const state = expectEnum(item.state, ["ready", "noop", "conflict", "unavailable"] as const, `${path}.state`);
    const action = item.action === undefined
      ? undefined
      : expectEnum(item.action, ["add", "update", "remove"] as const, `${path}.action`);
    expectOptionalStrings(item, ["path", "reason", "scopeDir"], path);
    expectOptionalEnum(item, "scope", ["project"] as const, path);
    return {
      harnessId,
      name,
      desired,
      state,
      ...(item.path === undefined ? {} : { path: item.path as string }),
      ...(action === undefined ? {} : { action }),
      ...(item.reason === undefined ? {} : { reason: item.reason as string }),
      ...(item.scope === undefined ? {} : { scope: item.scope as ScopeMode }),
      ...(item.scopeDir === undefined ? {} : { scopeDir: item.scopeDir as string }),
    };
  });
}

function parseResults(value: unknown): UpdateResult[] {
  return expectArray(value, "response.results").map((entry, index) => {
    const path = `response.results[${index}]`;
    const item = expectRecord(entry, path);
    expectEnum(item.harnessId, HARNESS_IDS, `${path}.harnessId`);
    expectString(item.name, `${path}.name`);
    expectEnum(item.desired, ["present", "absent"] as const, `${path}.desired`);
    expectEnum(item.state, ["applied", "noop", "skipped", "conflict", "failed"] as const, `${path}.state`);
    if (item.action !== undefined) expectEnum(item.action, ["add", "update", "remove"] as const, `${path}.action`);
    expectOptionalStrings(item, ["path", "reason", "scopeDir"], path);
    expectOptionalEnum(item, "scope", ["project"] as const, path);
    return item as unknown as UpdateResult;
  });
}

function applyScope(request: Request, scope: Scope | undefined): void {
  if (scope === undefined) return;
  if (scope.mode !== "project" || typeof scope.dir !== "string" || scope.dir.trim() === "") {
    throw new TypeError("project scope requires a directory");
  }
  request.scope = { mode: "project", dir: scope.dir.trim() };
}

function expectRecord(value: unknown, path: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new ProtocolValidationError(`${path} must be an object`);
  }
  return value as Record<string, unknown>;
}

function expectArray(value: unknown, path: string): unknown[] {
  if (!Array.isArray(value)) throw new ProtocolValidationError(`${path} must be an array`);
  return value;
}

function expectString(value: unknown, path: string): string {
  if (typeof value !== "string") throw new ProtocolValidationError(`${path} must be a string`);
  return value;
}

function expectEnum<const T extends readonly string[]>(value: unknown, allowed: T, path: string): T[number] {
  if (typeof value !== "string" || !allowed.includes(value)) {
    throw new ProtocolValidationError(`${path} must be one of ${allowed.join(", ")}`);
  }
  return value as T[number];
}

function expectOptionalStrings(item: Record<string, unknown>, keys: string[], path: string): void {
  for (const key of keys) {
    if (item[key] !== undefined) expectString(item[key], `${path}.${key}`);
  }
}

function expectOptionalEnum<const T extends readonly string[]>(
  item: Record<string, unknown>,
  key: string,
  allowed: T,
  path: string,
): void {
  if (item[key] !== undefined) expectEnum(item[key], allowed, `${path}.${key}`);
}

function expectBoolean(value: unknown, path: string): boolean {
  if (typeof value !== "boolean") throw new ProtocolValidationError(`${path} must be a boolean`);
  return value;
}

function expectOptionalProject(item: Record<string, unknown>, path: string): void {
  if (item.project === undefined) return;
  const projectPath = `${path}.project`;
  const project = expectRecord(item.project, projectPath);
  expectString(project.path, `${projectPath}.path`);
  expectString(project.reloadHint, `${projectPath}.reloadHint`);
  expectString(project.lifecycle, `${projectPath}.lifecycle`);
  expectBoolean(project.shareable, `${projectPath}.shareable`);
  expectBoolean(project.trustGate, `${projectPath}.trustGate`);
}

function expectOptionalStringArray(value: unknown, path: string): void {
  if (value === undefined) return;
  expectArray(value, path).forEach((entry, index) => expectString(entry, `${path}[${index}]`));
}

function diagnosticSuffix(stderr: string): string {
  const diagnostic = stderr.trim();
  return diagnostic === "" ? "" : `: ${diagnostic}`;
}
