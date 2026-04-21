// src/core/chat.ts
import { uuidv7 } from "../utils/uuid";
import { extractPlainText } from "./msg-utils";
import { Store } from "./store";
import type {
	ChatPlugin,
	ChatRequestParams,
	ChatSession,
	ChatState,
	ContentBlock,
	FinishReason,
	Message,
	ProviderAdapter,
	StorageAdapter,
	StreamEvent,
} from "./types";

export interface LLMChatConfig {
	provider: ProviderAdapter;
	storage: StorageAdapter;
	initialSessionId?: string | null;
}

export class LLMChat {
	public store: Store<ChatState>;

	private provider: ProviderAdapter;
	private storage: StorageAdapter;
	private plugins: ChatPlugin[] = [];

	private isFetchingSessions = false;
	private switchSeq = 0;

	constructor(config: LLMChatConfig) {
		this.provider = config.provider;
		this.storage = config.storage;

		const startingId = config.initialSessionId || uuidv7();

		this.store = new Store<ChatState>({
			sessions: [],
			hasMoreSessions: false,
			currentSessionId: startingId,
			messages: [],
			isGenerating: false,
			isLoadingSession: true,
			error: null,
		});

		this.init(startingId, !!config.initialSessionId);
	}

	public registerPlugins(plugins: ChatPlugin[]) {
		this.plugins = plugins;
	}

	private get state() {
		return this.store.get();
	}

	private get isBusy() {
		return this.store.get().isGenerating;
	}

	public setProvider(newProvider: ProviderAdapter) {
		if (this.isBusy) this.stopGeneration();
		this.provider = newProvider;
	}

	// Call this when the user scrolls to the bottom of the sidebar
	public async loadMoreSessions() {
		if (this.isFetchingSessions || !this.state.hasMoreSessions) return;

		this.isFetchingSessions = true;

		try {
			const sessions = this.state.sessions;
			const cursor = sessions.length > 0 ? sessions[sessions.length - 1].updatedAt : undefined;

			const result = await this.storage.loadSessions(20, cursor);

			if (result.items.length > 0) {
				this.store.set({
					sessions: [...this.state.sessions, ...result.items],
					hasMoreSessions: result.hasMore,
				});
			} else {
				this.store.set({ hasMoreSessions: false });
			}
		} catch (error) {
			console.error("Failed to load more sessions", error);
		} finally {
			this.isFetchingSessions = false;
		}
	}

	public async createNewSession() {
		if (this.isBusy) await this.stopGeneration();

		this.store.set({
			currentSessionId: uuidv7(),
			messages: [],
			error: null,
		});
	}

	public async switchSession(id: string) {
		if (this.state.currentSessionId === id) return;
		if (this.isBusy) await this.stopGeneration();

		const seq = ++this.switchSeq;

		this.store.set({
			currentSessionId: id,
			messages: [],
			isLoadingSession: true,
			error: null,
		});

		try {
			const session = await this.storage.loadOne(id);
			if (seq !== this.switchSeq) return; // stale

			// User may have navigated again while this one was loading
			if (this.state.currentSessionId !== id) return;

			if (!session) throw new Error("Chat not found");

			this.store.set({
				messages: session ? session.messages : [],
				isLoadingSession: false,
			});
		} catch (error) {
			console.error(`Failed to load session "${id}"`, error);
			if (seq !== this.switchSeq) return;
			if (this.state.currentSessionId !== id) return;

			this.store.set({
				messages: [],
				isLoadingSession: false,
				error: "Failed to load chat.",
			});
		}
	}

	public async deleteSession(id: string) {
		try {
			await this.storage.delete(id);

			const isCurrent = this.state.currentSessionId === id;

			this.store.set({
				sessions: this.state.sessions.filter((s) => s.id !== id),
			});

			if (isCurrent) {
				this.createNewSession();
			}
		} catch (error) {
			console.error(`Failed to delete session "${id}"`, error);
		}
	}

	public async sendMessage(content: string) {
		if (this.isBusy) return;

		// If the previous request failed and left a dead, empty assistant message, remove it.
		const currentMessages = this.pruneDeadMessages(this.state.messages);

		const userMsg: Message = {
			id: uuidv7(),
			role: "user",
			blocks: content ? [{ id: uuidv7(), type: "text", text: content }] : [],
		};

		for (const plugin of this.plugins) {
			if (plugin.onUserSubmit) {
				plugin.onUserSubmit(userMsg);
			}
		}

		this.startStreamingResponse([...currentMessages, userMsg]);
	}

	public async editAndResubmit(messageId: string, newContent: string) {
		if (this.isBusy) return;

		const currentMessages = this.state.messages;
		const targetIndex = currentMessages.findIndex((m) => m.id === messageId);

		if (targetIndex === -1) return;

		// Truncate history to remove everything AFTER the edited message
		// and update the edited message itself
		const updatedMessages = currentMessages.slice(0, targetIndex + 1);

		// Overwrite the message blocks with just the new text block
		updatedMessages[targetIndex] = {
			...updatedMessages[targetIndex],
			blocks: newContent ? [{ id: uuidv7(), type: "text", text: newContent }] : [],
		};

		this.startStreamingResponse(updatedMessages);
	}

	public async stopGeneration() {
		if (!this.isBusy) return;
		this.provider.abort();
		await this.finalizeStream({ wasAborted: true });
	}

	public async destroy() {
		await this.stopGeneration();
		if (this.storage.close) {
			this.storage.close();
		}
	}

	private async init(targetId: string, isFromUrl: boolean) {
		try {
			await this.loadInitialState(targetId, isFromUrl);
		} catch (error) {
			console.error("Storage Error during init:", error);
			this.store.set({
				isLoadingSession: false,
				error: "Failed to load history. Chatting in memory mode.",
			});
		}
	}

	private async loadInitialState(targetId: string, isFromUrl: boolean) {
		const result = await this.storage.loadSessions(20);
		const sessions = result.items;

		let activeSession: ChatSession | null = null;

		if (isFromUrl) {
			activeSession = await this.storage.loadOne(targetId);
			if (activeSession && !sessions.find((s) => s.id === targetId)) {
				// Inject metadata into sidebar if it wasn't in the first 20
				sessions.unshift({
					id: activeSession.id,
					title: activeSession.title,
					updatedAt: activeSession.updatedAt,
				});
			}
		} else if (sessions.length > 0) {
			// Default behavior: just load the most recent chat
			activeSession = await this.storage.loadOne(sessions[0].id);
		}

		if (activeSession) {
			this.store.set({
				sessions,
				hasMoreSessions: result.hasMore,
				currentSessionId: activeSession.id,
				messages: activeSession.messages,
				isLoadingSession: false,
			});
		} else {
			this.store.set({
				sessions,
				hasMoreSessions: result.hasMore,
				isLoadingSession: false,
			});
		}
	}

	private async startStreamingResponse(contextMessages: Message[]) {
		const pendingId = uuidv7();

		// Instantly create an empty assistant message so the UI shows a loading state
		const assistantMsg: Message = {
			id: pendingId,
			role: "assistant",
			blocks: [],
		};

		const updatedMessages = [...contextMessages, assistantMsg];

		this.store.set({
			messages: updatedMessages,
			isGenerating: true,
			error: null,
		});

		let payloadParams: ChatRequestParams = {
			messages: contextMessages.map((m) => ({ ...m })),
			options: {},
		};

		try {
			for (const plugin of this.plugins) {
				if (plugin.beforeSubmit) {
					payloadParams = await plugin.beforeSubmit(payloadParams);
				}
			}

			if (payloadParams.options.systemPrompt) {
				payloadParams.messages.unshift({
					id: uuidv7(),
					role: "system",
					blocks: [{ id: uuidv7(), type: "text", text: payloadParams.options.systemPrompt }],
				});
			}

			let finalReason: FinishReason = "stop";

			await this.provider.streamChat(payloadParams.messages, payloadParams.options, (event) => {
				if (event.type === "finish") finalReason = event.reason;
				this.handleStreamEvent(pendingId, event);
			});

			// Finish up stream
			await this.finalizeStream({ reason: finalReason });
		} catch (err: any) {
			this.finalizeStream({ error: err });
		}
	}

	/**
	 * High-performance state reducer. Bypasses cloning by mutating the active blocks.
	 * @param pendingId The ID we generated locally to track the active response.
	 */
	private handleStreamEvent(pendingId: string, event: StreamEvent) {
		this.store.mutate((state) => {
			const msg = state.messages.find((m) => m.id === pendingId);
			if (!msg) return; // Only happens if user rapidly deleted the chat during stream

			switch (event.type) {
				case "message_start":
					// We already pushed a placeholder. We can optionally merge metadata.
					if (event.message.meta) {
						msg.meta = { ...msg.meta, ...event.message.meta };
					}
					break;

				case "text_delta": {
					let tb = msg.blocks.find((b) => b.id === event.blockId) as Extract<ContentBlock, { type: "text" }>;
					if (!tb) {
						tb = { id: event.blockId, type: "text", text: "" };
						msg.blocks.push(tb);
					}
					tb.text += event.delta;
					break;
				}

				case "reasoning_delta": {
					let rb = msg.blocks.find((b) => b.id === event.blockId) as Extract<ContentBlock, { type: "reasoning" }>;
					if (!rb) {
						rb = { id: event.blockId, type: "reasoning", text: "", encrypted: event.encrypted };
						msg.blocks.push(rb);
					}
					rb.text += event.delta;
					break;
				}

				case "tool_call_start":
					msg.blocks.push(event.block);
					break;

				case "tool_call_delta": {
					const tcb = msg.blocks.find((b) => b.id === event.blockId) as Extract<ContentBlock, { type: "tool_call" }>;
					if (tcb) {
						if (event.argsDelta) tcb.argsText += event.argsDelta;
						if (event.status) tcb.status = event.status;
					}
					break;
				}

				case "tool_result":
				case "artifact":
					msg.blocks.push(event.block);
					break;

				// Usage, finish, error handled mostly outside mutation or discarded
				case "error":
					state.error = event.message;
					break;
			}
		});
	}

	private async finalizeStream(opts: { error?: Error; wasAborted?: boolean; reason?: FinishReason }) {
		if (!this.isBusy) return;
		this.store.set({ isGenerating: false });

		try {
			if (opts.error) {
				const updatedMessages = this.pruneDeadMessages(this.state.messages);
				this.store.set({
					error: opts.error.message,
					messages: updatedMessages,
				});
			}

			await this.persistCurrentSession();

			const currentMsgs = this.state.messages;
			const sessionId = this.state.currentSessionId;

			// Auto-title trigger
			if (!opts.error && !opts.wasAborted && this.provider.generateTitle) {
				const assistantRepliesCount = currentMsgs.filter((m) => m.role === "assistant" && m.blocks.length > 0).length;

				if (assistantRepliesCount === 1) {
					void this.triggerAutoTitle(sessionId, currentMsgs);
				}
			}
		} catch (error) {
			console.error("Failed to finalize stream", error);
		}
	}

	private async persistCurrentSession(): Promise<boolean> {
		const { currentSessionId, messages, sessions } = this.store.get();
		if (messages.length === 0) return true;

		const existingMeta = sessions.find((s) => s.id === currentSessionId);
		let title = existingMeta?.title;

		if (!title) {
			const firstMsg = messages[0];
			const text = extractPlainText(firstMsg);
			if (text.trim().length > 0) {
				title = text.length > 30 ? text.slice(0, 30) + "..." : text;
			} else if (firstMsg.blocks.some((b) => b.type === "file")) {
				const fileBlock = firstMsg.blocks.find((b) => b.type === "file") as any;
				title = `File: ${fileBlock.name || "Upload"}`;
			} else {
				title = "New Chat";
			}
		}

		const updatedAt = Date.now();

		const sessionToSave: ChatSession = {
			id: currentSessionId,
			title,
			updatedAt,
			messages,
		};

		try {
			await this.storage.save(sessionToSave);

			this.store.set({
				sessions: [
					{ id: currentSessionId, title, updatedAt },
					...this.state.sessions.filter((s) => s.id !== currentSessionId),
				],
			});

			return true;
		} catch (error) {
			console.error(`Failed to persist session "${currentSessionId}"`, error);
			return false;
		}
	}

	private async triggerAutoTitle(sessionId: string, messages: Message[]) {
		try {
			let payloadParams: ChatRequestParams = {
				messages: messages.map((m) => ({ ...m })),
				options: {},
			};

			for (const plugin of this.plugins) {
				if (plugin.beforeSubmit) {
					payloadParams = await plugin.beforeSubmit(payloadParams);
				}
			}

			const smartTitle = await this.provider.generateTitle!(payloadParams.messages, payloadParams.options);
			if (!smartTitle) return;

			// user may have deleted this session while the title was generating in the background.
			if (!this.state.sessions.find((s) => s.id === sessionId)) return;

			if (this.storage.updateMetadata) {
				await this.storage.updateMetadata(sessionId, { title: smartTitle });
			}

			this.store.set({
				sessions: this.state.sessions.map((s) => (s.id === sessionId ? { ...s, title: smartTitle } : s)),
			});
		} catch (e) {
			console.error("Failed to auto-generate title", e);
		}
	}

	private pruneDeadMessages(messages: Message[]): Message[] {
		const last = messages.at(-1);
		// If the last assistant message has absolutely no blocks, it failed before TTFB. Remove it.
		if (last && last.role === "assistant" && last.blocks.length === 0) {
			return messages.slice(0, -1);
		}
		return messages;
	}
}
