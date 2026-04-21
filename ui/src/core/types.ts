import type { LLMChat } from "./chat";

export type JsonValue = string | number | boolean | null | { [key: string]: JsonValue } | JsonValue[];

export type ContentBlock =
	| {
		id: string;
		type: "text";
		text: string;
	}
	| {
		id: string;
		type: "reasoning";
		text: string;
		encrypted?: boolean;
	}
	| {
		id: string;
		type: "tool_call";
		toolCallId: string;
		name: string;
		argsText: string;
		status: "streaming" | "pending" | "running" | "complete" | "error";
	}
	| {
		id: string;
		type: "tool_result";
		toolCallId: string;
		outputText: string;
		isError?: boolean;
	}
	| {
		id: string;
		type: "artifact";
		artifactId: string;
		mime: string;
		title?: string;
		content: string;
	}
	| {
		id: string;
		type: "file";
		mimeType: string;
		name?: string;
		data: string;
	};

export type Role = "system" | "user" | "assistant" | "tool";

export interface Message {
	id: string;
	role: Role;
	blocks: ContentBlock[];
	meta?: {
		source?: "remote" | "local";
		name?: string;
		createdAt?: number;
		ephemeral?: boolean;
	};
}

export type FinishReason = "stop" | "length" | "tool_use" | "content_filter" | "error" | "aborted";

export type StreamEvent =
	| {
		type: "message_start";
		message: Message;
	}
	| {
		type: "text_delta";
		messageId: string;
		blockId: string;
		delta: string;
	}
	| {
		type: "reasoning_delta";
		messageId: string;
		blockId: string;
		delta: string;
		encrypted?: boolean;
	}
	| {
		type: "tool_call_start";
		messageId: string;
		block: Extract<ContentBlock, { type: "tool_call" }>;
	}
	| {
		type: "tool_call_delta";
		messageId: string;
		blockId: string;
		name?: string;
		argsDelta?: string;
		status?: Extract<ContentBlock, { type: "tool_call" }>["status"];
	}
	| {
		type: "tool_result";
		messageId: string;
		block: Extract<ContentBlock, { type: "tool_result" }>;
	}
	| {
		type: "artifact";
		messageId: string;
		block: Extract<ContentBlock, { type: "artifact" }>;
	}
	| {
		type: "usage";
		input: number;
		output: number;
		cacheRead?: number;
		cacheWrite?: number;
	}
	| {
		type: "error";
		message: string;
		code?: string;
		retryable?: boolean;
	}
	| {
		type: "finish";
		reason: FinishReason;
	};

export interface ChatSessionMeta {
	id: string;
	title: string;
	updatedAt: number;
}

export interface ChatSession {
	id: string;
	title: string;
	updatedAt: number;
	messages: Message[];
}

export interface PaginatedSessions {
	items: ChatSessionMeta[];
	hasMore: boolean;
}

export interface ChatState {
	sessions: ChatSessionMeta[];
	hasMoreSessions: boolean;
	currentSessionId: string;
	messages: Message[];
	generatingMessageId: string | null;
	isLoadingSession: boolean;
	error: { message: string; id?: string } | null;
}

export interface StorageAdapter {
	loadSessions(limit: number, cursor?: number): Promise<PaginatedSessions>;
	loadOne(id: string): Promise<ChatSession | null>;
	save(session: ChatSession): Promise<void>;
	updateMetadata?(id: string, meta: Partial<ChatSessionMeta>): Promise<void>;
	delete(id: string): Promise<void>;
	close?(): void | Promise<void>;
}

export interface RequestOptions {
	model?: string;
	systemPrompt?: string;
	temperature?: number;
	top_p?: number;
	max_tokens?: number;
	tools?: Record<string, unknown>[];
	stream_options?: Record<string, unknown>;
	[key: string]: unknown;
}

export interface ChatRequestParams {
	messages: Message[];
	options: RequestOptions;
}

export interface ProviderAdapter {
	streamChat(messages: Message[], options: RequestOptions, onEvent: (event: StreamEvent) => void): Promise<void>;

	abort(): void;

	generateTitle?(messages: Message[], options?: RequestOptions): Promise<string>;
}

export interface RenderConfig {
	highlighter?: (code: string, lang: string) => string;
	plugins: ChatPlugin[];
}

export interface PluginContext {
	chat: LLMChat;
	elements: {
		container: HTMLElement;
		sidebar: HTMLElement;
		header: HTMLElement;
	};
}

export interface PluginInputContext {
	form: HTMLFormElement;
}

export interface ChatPlugin {
	name: string;

	// --- Global Lifecycle ---
	/** Fires once when the UI boots. Gives access to major DOM sections. */
	onMount?: (ctx: PluginContext) => void;
	/** Cleanup function when the chat instance is destroyed. */
	destroy?: () => void;

	// --- Request / Network ---
	/** Intercept and mutate the request (messages, model, etc.) before sending. */
	beforeSubmit?: (params: ChatRequestParams) => ChatRequestParams | Promise<ChatRequestParams>;

	// --- Input Area ---
	/** Fires when the input form mounts. Use to add buttons or custom UI. */
	onInputMount?: (ctx: PluginInputContext) => void;
	/** Tell the input area to allow submission even if the text area is empty. */
	hasPendingData?: () => boolean;
	/** Mutate the newly created user message before it gets saved and sent. */
	onUserSubmit?: (msg: Message) => void;

	// --- Feed Area ---
	/** Hook to mutate or attach UI to a message node as it renders. */
	onMessageRender?: (msg: Message, parentEl: HTMLElement, isGenerating: boolean) => void;
}
