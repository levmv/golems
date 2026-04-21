import { marked } from "marked";
import { hasTextContent } from "../core/msg-utils";
import type { Message, RenderConfig } from "../core/types";
import { el, syncDOM } from "../utils/dom";
import { renderSafeHTML } from "../utils/html";
import { ICON_CHECK, ICON_COPY } from "../utils/icons";

const MARKDOWN_THROTTLE_MS = 70;

export class MessageNode {
	public readonly el: HTMLElement;

	private contentEl?: HTMLElement;
	private loadingEl?: HTMLElement;
	private errorEl?: HTMLElement;
	private actionsEl?: HTMLElement;

	private cacheContent = "";
	private cacheError: string | null = null;
	private cacheActionDisplay: string = "";

	private isDestroyed = false;
	// Prevents stale async markdown parses from overwriting newer streamed text
	private renderSeq = 0;
	private renderTimer: number | null = null;

	constructor(
		msg: Message,
		private config: RenderConfig,
	) {
		this.el = document.createElement("div");
		this.el.className = `message ${msg.role}`;
		if (msg.role === "assistant") {
			this.el.setAttribute("role", "article");
			this.el.setAttribute("aria-label", "AI response");
		}
	}

	public update(msg: Message, isLast: boolean, isGenerating: boolean, error: string | null) {
		this.renderLoading(msg);
		this.renderText(msg, isGenerating, isLast);
		this.renderActions(msg, isGenerating, isLast);
		this.renderError(isLast ? error : null);

		for (const plugin of this.config.plugins) {
			if (plugin.onMessageRender) {
				plugin.onMessageRender(msg, this.el, isGenerating);
			}
		}
	}

	public destroy() {
		this.isDestroyed = true;
		if (this.renderTimer) clearTimeout(this.renderTimer);
		this.el.remove();
	}

	private renderLoading(msg: Message) {
		const isLoading = msg.role === "assistant" && !hasTextContent(msg);

		if (isLoading) {
			if (!this.loadingEl) {
				this.loadingEl = el("div", "message-loading", {
					innerHTML: `<span class="dot"></span><span class="dot"></span><span class="dot"></span>`,
				});
				this.el.appendChild(this.loadingEl);
			}
		} else if (this.loadingEl) {
			this.loadingEl.remove();
			this.loadingEl = undefined;
		}
	}

	private renderText(msg: Message, isGenerating: boolean, isLast: boolean) {
		if (!msg.content || this.cacheContent === msg.content) return;

		const isActivelyTyping = isGenerating && isLast;

		// instant render: old messages, or chats loaded from history
		if (!isActivelyTyping) {
			if (this.renderTimer) {
				clearTimeout(this.renderTimer);
				this.renderTimer = null;
			}
			this.applyMarkdown(msg.content, ++this.renderSeq);
			return;
		}

		if (this.renderTimer) return;

		this.renderTimer = window.setTimeout(() => {
			this.renderTimer = null;
			this.applyMarkdown(msg.content, ++this.renderSeq);
		}, MARKDOWN_THROTTLE_MS);
	}

	private renderError(error: string | null) {
		if (!error) {
			if (this.errorEl) {
				this.errorEl.style.display = "none";
			}
			this.cacheError = null;
			return;
		}

		if (!this.errorEl) {
			this.errorEl = el("div", "message-error");
			this.el.appendChild(this.errorEl);
		}

		if (this.cacheError !== error) {
			this.errorEl.textContent = `⚠ ${error}`;
			this.errorEl.style.display = "flex";
			this.cacheError = error;
		}
	}

	private renderActions(msg: Message, isGenerating: boolean, isLast: boolean) {
		if (msg.role !== "assistant") return;

		if (!this.actionsEl) {
			const actionButtons: HTMLElement[] = [];

			if (typeof navigator !== "undefined" && navigator.clipboard) {
				const copyBtn = el("button", "action-icon-btn", {
					title: "Copy message",
					innerHTML: ICON_COPY,
				});
				copyBtn.addEventListener("click", async () => {
					try {
						await navigator.clipboard.writeText(this.cacheContent);
						copyBtn.innerHTML = ICON_CHECK;
						setTimeout(() => (copyBtn.innerHTML = ICON_COPY), 2000);
					} catch {
						// Ignore partial failures (like denied permissions)
					}
				});
				actionButtons.push(copyBtn);
			}

			this.actionsEl = el("div", "message-actions", null, actionButtons);
			this.el.appendChild(this.actionsEl);
		}

		const isActivelyTyping = isGenerating && isLast;
		const targetDisplay = isActivelyTyping ? "none" : "flex";

		if (this.cacheActionDisplay !== targetDisplay) {
			this.actionsEl.style.display = targetDisplay;
			this.cacheActionDisplay = targetDisplay;
		}
	}

	private async applyMarkdown(content: string, seq: number) {
		const html = await marked.parse(content);
		if (this.isDestroyed) return;
		// renderSeq bumped on each render request so slower async markdown parses
		// cannot overwrite newer streamed content.
		if (seq !== this.renderSeq) return;

		if (!this.contentEl) {
			this.contentEl = el("div", "message-content");
			renderSafeHTML(this.contentEl, html, this.config.highlighter);
			this.el.appendChild(this.contentEl);
		} else {
			const tempDiv = el("div", "message-content");
			renderSafeHTML(tempDiv, html, this.config.highlighter);
			syncDOM(this.contentEl, tempDiv);
		}

		this.cacheContent = content;
	}
}
