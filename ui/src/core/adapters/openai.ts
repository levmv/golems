import { parseSSE } from "../../utils/sse";
import type { Message, ProviderAdapter, RequestOptions, StreamChunk } from "../types";

type OpenAIContentPart = { type: "text"; text: string } | { type: "image_url"; image_url: { url: string } };

export class OpenAIAdapter implements ProviderAdapter {
	private abortController: AbortController | null = null;

	constructor(
		private apiKey: string,
		private endpoint: string,
		private model: string,
	) { }

	async streamChat(
		messages: Message[],
		options: RequestOptions,
		onChunk: (chunk: StreamChunk) => void,
		onDone: () => void,
		onError: (err: Error) => void,
	): Promise<void> {
		this.abortController = new AbortController();

		try {
			const {
				model = this.model,
				systemPrompt, // Extracted so it isn't spread into the body
				...restOptions
			} = options;

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

			if (!response.ok) throw new Error(`API Error: ${response.status}`);
			await parseSSE(response, (data) => {
				if (data === "[DONE]") return true;

				// 2. Parse the OpenAI specific JSON
				try {
					const parsed = JSON.parse(data);
					const choice = parsed.choices?.[0] ?? {};
					const delta = choice.delta ?? {};

					const content = delta.content || "";

					let reasoning = "";
					const reasoningFields = ["reasoning_content", "reasoning", "reasoning_text"];
					for (const field of reasoningFields) {
						if (typeof delta[field] === "string" && delta[field].length > 0) {
							reasoning = delta[field];
							break;
						}
					}
					const reasoningEncrypted = this.extractEncryptedReasoning(delta);

					if (content || reasoning || reasoningEncrypted) {
						onChunk({ content, reasoning, reasoningEncrypted });
					}
				} catch {
					// Ignore partial/broken JSON payload
				}
			});

			onDone();
		} catch (err: any) {
			if (err?.name === "AbortError") {
				console.log("Stream aborted by user.");
			} else {
				onError(err);
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
			// Find the index of the first assistant message
			let endIndex = messages.findIndex((m) => m.role === "assistant" && m.content);

			// Fallback: if somehow no assistant message exists, just take up to the first 4 messages
			if (endIndex === -1) endIndex = Math.min(messages.length - 1, 3);

			const contextMessages = messages.slice(0, endIndex + 1).map(({ role, content }) => ({
				role,
				content: content.slice(0, 800), // Truncate to prevent huge token usage
			}));

			const modelToUse = options?.model || this.model;

			const response = await fetch(this.endpoint, {
				method: "POST",
				headers: {
					"Content-Type": "application/json",
					Authorization: `Bearer ${this.apiKey}`,
				},
				body: JSON.stringify({
					model: modelToUse,
					messages: [
						...contextMessages,
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
			if (!msg.attachments || msg.attachments.length === 0) {
				return { role: msg.role, content: msg.content };
			}

			const textFiles = msg.attachments.filter((a) => a.type === "file");
			let combinedText = msg.content;
			if (textFiles.length > 0) {
				combinedText += textFiles.map((f) => `\n\n--- File: ${f.name} ---\n${f.data}`).join("");
			}

			const images = msg.attachments.filter((a) => a.type === "image");
			// If there were only text files and no images, return standard string format
			if (images.length === 0) {
				return { role: msg.role, content: combinedText };
			}

			// If there are images, OpenAI requires the array format
			const contentParts: OpenAIContentPart[] = [];
			if (combinedText) contentParts.push({ type: "text", text: combinedText });
			for (const img of images) {
				contentParts.push({ type: "image_url", image_url: { url: img.data } });
			}

			return { role: msg.role, content: contentParts };
		});
	}

	private extractEncryptedReasoning(delta: any): string {
		if (typeof delta?.reasoning?.encrypted === "string") return delta.reasoning.encrypted;
		if (typeof delta?.reasoning_encrypted === "string") return delta.reasoning_encrypted;
		return "";
	}
}
