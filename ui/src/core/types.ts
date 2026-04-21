import type { LLMChat } from "./chat";

export type Role = "system" | "user" | "assistant";

export interface Message {
	id: string;
	role: Role;
	content: string;
	reasoning?: string;
	reasoningEncrypted?: string;
	attachments?: Attachment[];
}

export type StreamChunk = {
	content: string;
	reasoning: string;
	reasoningEncrypted?: string;
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
	isGenerating: boolean;
	isLoadingSession: boolean;
	error: string | null;
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
	tools?: any[];
	stream_options?: Record<string, unknown>;
	[key: string]: unknown;
}

export interface ChatRequestParams {
	messages: Message[];
	options: RequestOptions;
}

export interface ProviderAdapter {
	streamChat(
		messages: Message[],
		options: RequestOptions,
		onChunk: (chunk: StreamChunk) => void,
		onDone: () => void,
		onError: (err: Error) => void,
	): Promise<void>;

	abort(): void;

	generateTitle?(messages: Message[], options?: RequestOptions): Promise<string>;
}

export interface RenderConfig {
	highlighter?: (code: string, lang: string) => string;
	plugins: ChatPlugin[];
}

export interface Attachment {
	type: "image" | "file";
	name: string;
	data: string;
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
