import "./thinking.css";
import { getDisplayReasoning, hasReasoning } from "../../core/msg-utils";
import type { ChatPlugin } from "../../core/types";
import { el } from "../../utils/dom";
import { renderSafeHTML } from "../../utils/html";
import { ICON_CHEVRON } from "../../utils/icons";

interface ThinkingState {
	isExpanded: boolean;
	cacheReasoning: string;
	cacheIsGenerating: boolean;
	contentEl: HTMLElement;
	btnSpan: HTMLElement;
	svgIcon: SVGElement;
}

export function ThinkingPlugin(): ChatPlugin {
	const stateMap = new WeakMap<HTMLElement, ThinkingState>();

	return {
		name: "thinking",
		onMessageRender: (msg, parentEl, isGenerating) => {
			if (!hasReasoning(msg)) return;

			// Find the block container created by MessageNode
			const reasoningBlockEl = parentEl.querySelector(".block-reasoning") as HTMLElement;
			if (!reasoningBlockEl) return;

			let state = stateMap.get(reasoningBlockEl);

			if (!state) {
				const btn = el("button", "think-toggle", {
					innerHTML: ICON_CHEVRON + "<span>Thought Process</span>",
				});

				const btnSpan = btn.querySelector("span") as HTMLElement;
				const svgIcon = btn.querySelector("svg") as SVGElement;

				const contentEl = el("div", "think-content");
				contentEl.style.display = "none";

				const wrapper = el("div", "think-wrapper", {}, [btn, contentEl]);

				// Clear the raw text placeholder inserted by MessageNode and append our UI
				reasoningBlockEl.innerHTML = "";
				reasoningBlockEl.appendChild(wrapper);

				state = {
					isExpanded: false,
					cacheReasoning: "",
					cacheIsGenerating: false,
					contentEl,
					btnSpan,
					svgIcon,
				};

				btn.onclick = () => {
					state!.isExpanded = !state!.isExpanded;
					contentEl.style.display = state!.isExpanded ? "block" : "none";
					svgIcon.style.transform = `rotate(${state!.isExpanded ? "90deg" : "0deg"})`;

					const displayContent = getDisplayReasoning(msg);

					if (state!.isExpanded && state!.cacheReasoning !== displayContent) {
						renderSafeHTML(contentEl, displayContent);
						state!.cacheReasoning = displayContent;
					}
				};

				stateMap.set(reasoningBlockEl, state);
			}

			// We determine "Actively thinking" if generating and there's NO text blocks yet
			const hasText = msg.blocks.some((b) => b.type === "text" && b.text.trim().length > 0);
			const isActivelyThinking = isGenerating && !hasText;

			if (state.cacheIsGenerating !== isActivelyThinking) {
				state.btnSpan.textContent = isActivelyThinking ? "Thinking..." : "Thought Process";
				state.cacheIsGenerating = isActivelyThinking;
			}

			if (isActivelyThinking) {
				parentEl.classList.add("active-thinking");
			} else {
				parentEl.classList.remove("active-thinking");
			}

			const displayContent = getDisplayReasoning(msg);

			if (state.isExpanded && state.cacheReasoning !== displayContent) {
				renderSafeHTML(state.contentEl, displayContent);
				state.cacheReasoning = displayContent;
			}
		},
	};
}
