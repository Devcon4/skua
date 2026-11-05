export interface Profile {
	name: string;
	description: string;
	models: string[];
	ready: boolean;
}

export interface Workspace {
	repo: string;
	baseRef: string;
}

export interface CreateRunBody {
	profile: string;
	clientKey: string;
	workspace?: Workspace;
	task: { prompt: string; schema?: unknown };
}

export type RunPhase = "Pending" | "Running" | "Succeeded" | "Failed";

export interface RunOutputs {
	branch?: string;
	pullRequest?: string | number;
}

export interface Run {
	name: string;
	profile: string;
	clientKey: string;
	phase: RunPhase;
	reason: string;
	result: unknown;
	outputs: RunOutputs;
	createdAt: string;
}

export type RunEvent =
	| { seq: number; type: "text"; data: { delta: string } }
	| { seq: number; type: "thinking"; data: { delta: string } }
	| { seq: number; type: "tool_start"; data: { id: string; name: string; args: unknown } }
	| { seq: number; type: "tool_update"; data: { id: string; partial: unknown } }
	| { seq: number; type: "tool_end"; data: { id: string; result: unknown; isError: boolean } }
	| { seq: number; type: "status"; data: { phase: RunPhase; reason?: string } }
	| { seq: number; type: "end"; data: { phase: RunPhase; reason: string; result: unknown; outputs: RunOutputs } };

export type RunEventType = RunEvent["type"];

const PHASES: readonly string[] = ["Pending", "Running", "Succeeded", "Failed"];

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isPhase(v: unknown): v is RunPhase {
	return typeof v === "string" && PHASES.includes(v);
}

function isOutputs(v: unknown): v is RunOutputs {
	if (!isObject(v)) return false;
	if (v.branch !== undefined && typeof v.branch !== "string") return false;
	if (v.pullRequest !== undefined && typeof v.pullRequest !== "string" && typeof v.pullRequest !== "number") return false;
	return true;
}

export function isProfile(v: unknown): v is Profile {
	return (
		isObject(v) &&
		typeof v.name === "string" &&
		typeof v.description === "string" &&
		Array.isArray(v.models) &&
		v.models.every(m => typeof m === "string") &&
		typeof v.ready === "boolean"
	);
}

export function isRun(v: unknown): v is Run {
	return (
		isObject(v) &&
		typeof v.name === "string" &&
		typeof v.profile === "string" &&
		typeof v.clientKey === "string" &&
		isPhase(v.phase) &&
		typeof v.reason === "string" &&
		isOutputs(v.outputs) &&
		typeof v.createdAt === "string"
	);
}

function isEventData(type: unknown, d: unknown): boolean {
	if (!isObject(d)) return false;
	switch (type) {
		case "text":
		case "thinking":
			return typeof d.delta === "string";
		case "tool_start":
			return typeof d.id === "string" && typeof d.name === "string";
		case "tool_update":
			return typeof d.id === "string";
		case "tool_end":
			return typeof d.id === "string" && typeof d.isError === "boolean";
		case "status":
			return isPhase(d.phase) && (d.reason === undefined || typeof d.reason === "string");
		case "end":
			return isPhase(d.phase) && typeof d.reason === "string" && isOutputs(d.outputs);
		default:
			return false;
	}
}

export function isRunEvent(v: unknown): v is RunEvent {
	return isObject(v) && typeof v.seq === "number" && Number.isInteger(v.seq) && isEventData(v.type, v.data);
}
