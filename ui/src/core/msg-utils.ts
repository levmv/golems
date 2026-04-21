import type { Message } from "./types";

/** Checks if the message has standard or encrypted reasoning */
export function hasReasoning(msg: Message): boolean {
	return !!msg.reasoning?.trim() || !!msg.reasoningEncrypted?.trim();
}

/** Returns the reasoning text, or a safe fallback for encrypted content */
export function getDisplayReasoning(msg: Message): string {
	if (msg.reasoning) return msg.reasoning;
	if (msg.reasoningEncrypted) {
		return "<i>Thought process is hidden by the model provider.</i>";
	}
	return "";
}

/** Checks if the message has standard visible text content */
export function hasTextContent(msg: Message): boolean {
	return !!msg.content?.trim();
}
