import { parseSSE } from "../../utils/sse";
import { uuidv7 } from "../../utils/uuid";
import type { ContentBlock, Message, ProviderAdapter, RequestOptions, StreamEvent } from "../types";

export class OpenAIAdapter implements ProviderAdapter {
	private abortController: AbortController | null = null;

	constructor(
		private apiKey: string,
		private endpoint: string,
		private model: string,
	) { }

	async streamChat(messages: Message[], options: RequestOptions, onEvent: (event: StreamEvent) => void): Promise<void> {
		this.abortController = new AbortController();

		try {
			const { model = this.model, systemPrompt, ...restOptions } = options;

			const response = await fetch(this.endpoint, {
				method: "POST",
				headers: {
					"Content-Type": "application/json",
					Authorization: `Bearer ${this.apiKey}`,
				},
				body: JSON.stringify({
					model: model,
					messages: this.formatMessages(messages),
					stream: true,
					...restOptions,
				}),
				signal: this.abortController.signal,
			});

			if (!response.ok) {
				const text = await response.text();
				throw new Error(`API Error ${response.status}: ${text}`);
			}

			let messageStarted = false;
			let currentMessageId = uuidv7();
			let currentTextBlockId: string | null = null;
			let currentReasoningBlockId: string | null = null;

			// Map OpenAI's tool call index to our block IDs
			const activeToolCalls = new Map<number, string>();

			await parseSSE(response, (data) => {
				if (data === "[DONE]") return true;

				try {
					const parsed = JSON.parse(data);
					const choice = parsed.choices?.[0];
					if (!choice) return;

					// 1. Emit start event on first chunk
					if (!messageStarted) {
						currentMessageId = parsed.id || currentMessageId;
						onEvent({
							type: "message_start",
							message: { id: currentMessageId, role: "assistant", blocks: [] },
						});
						messageStarted = true;
					}

					const delta = choice.delta ?? {};

					// 2. Handle Reasoning
					const reasoningData = this.extractReasoning(delta);
					if (reasoningData) {
						if (!currentReasoningBlockId) currentReasoningBlockId = uuidv7();
						currentTextBlockId = null;

						onEvent({
							type: "reasoning_delta",
							messageId: currentMessageId,
							blockId: currentReasoningBlockId,
							delta: reasoningData.text,
							encrypted: reasoningData.encrypted,
						});
					}

					// 3. Handle Text Content
					if (delta.content) {
						if (!currentTextBlockId) currentTextBlockId = uuidv7();
						onEvent({
							type: "text_delta",
							messageId: currentMessageId,
							blockId: currentTextBlockId,
							delta: delta.content,
						});
					}

					// 4. Handle Tool Calls
					if (delta.tool_calls && Array.isArray(delta.tool_calls)) {
						for (const tc of delta.tool_calls) {
							const index = tc.index;

							// If it has an ID, it's a new tool call
							if (tc.id) {
								currentTextBlockId = null;

								const blockId = uuidv7();
								activeToolCalls.set(index, blockId);
								onEvent({
									type: "tool_call_start",
									messageId: currentMessageId,
									block: {
										id: blockId,
										type: "tool_call",
										toolCallId: tc.id,
										name: tc.function?.name || "",
										argsText: tc.function?.arguments || "",
										status: "streaming",
									},
								});
							}
							// Otherwise, it's appending arguments to an existing tool call
							else if (activeToolCalls.has(index)) {
								onEvent({
									type: "tool_call_delta",
									messageId: currentMessageId,
									blockId: activeToolCalls.get(index)!,
									argsDelta: tc.function?.arguments || "",
								});
							}
						}
					}

					// 5. Handle Finish Reason
					if (choice.finish_reason) {
						const reasonMap: Record<string, any> = {
							stop: "stop",
							length: "length",
							tool_calls: "tool_use",
							content_filter: "content_filter",
						};
						onEvent({
							type: "finish",
							reason: reasonMap[choice.finish_reason] || "stop",
						});
					}
				} catch {
					// Ignore partial/broken JSON payload
				}
			});

			// If it finishes normally but didn't emit a finish reason (some providers do this)
			onEvent({ type: "finish", reason: "stop" });
		} catch (err: any) {
			if (err?.name === "AbortError") {
				onEvent({ type: "finish", reason: "aborted" });
			} else {
				onEvent({ type: "error", message: err.message || "Unknown error" });
			}
		} finally {
			this.abortController = null;
		}
	}

	abort(): void {
		if (this.abortController) {
			this.abortController.abort();
		}
	}

	async generateTitle(messages: Message[], options?: RequestOptions): Promise<string> {
		try {
			let endIndex = messages.findIndex((m) => m.role === "assistant" && m.blocks.length > 0);
			if (endIndex === -1) endIndex = Math.min(messages.length - 1, 3);

			const contextMessages = messages.slice(0, endIndex + 1);

			const response = await fetch(this.endpoint, {
				method: "POST",
				headers: {
					"Content-Type": "application/json",
					Authorization: `Bearer ${this.apiKey}`,
				},
				body: JSON.stringify({
					model: options?.model || this.model,
					messages: [
						...this.formatMessages(contextMessages),
						{
							role: "user",
							content:
								"Summarize the above conversation in 3-5 words. Reply ONLY with the title, no quotes, no extra text.",
						},
					],
					stream: false,
				}),
			});

			if (!response.ok) return "";
			const data = await response.json();
			return data.choices[0]?.message?.content?.trim() || "";
		} catch (e) {
			return "";
		}
	}

	private formatMessages(messages: Message[]) {
		return messages.map((msg) => {
			const toolResults = msg.blocks.filter((b) => b.type === "tool_result") as Extract<ContentBlock, { type: "tool_result" }>[];
			const toolCalls = msg.blocks.filter((b) => b.type === "tool_call") as Extract<ContentBlock, { type: "tool_call" }>[];

			if (msg.role === "tool" && toolResults.length > 0) {
				return {
					role: "tool",
					tool_call_id: toolResults[0].toolCallId,
					content: toolResults[0].outputText,
				};
			}

			const payload: any = { role: msg.role };

			// Handle Tool Calls (Role: Assistant)
			if (toolCalls.length > 0) {
				payload.tool_calls = toolCalls.map((tc) => ({
					id: tc.toolCallId,
					type: "function",
					function: { name: tc.name, arguments: tc.argsText },
				}));
			}

			// Sequentially map content blocks to preserve true multi-modal order
			const contentArray: any[] = [];

			for (const block of msg.blocks) {
				if (block.type === "text") {
					contentArray.push({ type: "text", text: block.text });
				} else if (block.type === "file") {
					if (block.mimeType.startsWith("image/")) {
						contentArray.push({ type: "image_url", image_url: { url: block.data } });
					} else {
						// Inject text files as text blocks in the exact sequence they appeared
						contentArray.push({ type: "text", text: `\n\n--- File: ${block.name} ---\n${block.data}` });
					}
				}
			}

			// OpenAI accepts a string for pure text, but requires an array for multi-modal.
			// We collapse to a string if there's only one text block to keep payloads clean.
			if (contentArray.length === 0) {
				payload.content = "";
			} else if (contentArray.length === 1 && contentArray[0].type === "text") {
				payload.content = contentArray[0].text;
			} else {
				payload.content = contentArray;
			}

			return payload;
		});
	}

	private extractReasoning(delta: any): { text: string; encrypted: boolean } | null {
		// Check for encrypted reasoning (e.g., Anthropic via OpenRouter / Some DeepSeek setups)
		if (typeof delta.reasoning?.encrypted === "string") {
			return { text: delta.reasoning.encrypted, encrypted: true };
		}
		if (typeof delta.reasoning_encrypted === "string") {
			return { text: delta.reasoning_encrypted, encrypted: true };
		}

		// Check for standard reasoning (DeepSeek R1, O1, etc.)
		const reasoningFields = ["reasoning_content", "reasoning", "reasoning_text"];
		for (const field of reasoningFields) {
			if (typeof delta[field] === "string" && delta[field].length > 0) {
				return { text: delta[field], encrypted: false };
			}
		}

		return null;
	}
}
