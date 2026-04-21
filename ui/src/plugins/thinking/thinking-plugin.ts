import "./thinking.css";
import { getDisplayReasoning, hasReasoning, hasTextContent } from "../../core/msg-utils";
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
			if (!hasReasoning(msg) && !stateMap.has(parentEl)) return;

			let state = stateMap.get(parentEl);

			if (!state) {
				const btn = el("button", "think-toggle", {
					innerHTML: ICON_CHEVRON + "<span>Thought Process</span>",
				});

				const btnSpan = btn.querySelector("span") as HTMLElement;
				const svgIcon = btn.querySelector("svg") as SVGElement;

				const contentEl = el("div", "think-content");
				contentEl.style.display = "none";

				const wrapper = el("div", "think-wrapper", {}, [btn, contentEl]);
				wrapper.style.order = "1";
				parentEl.appendChild(wrapper);

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

					// Render the text if expanded for the first time
					if (state!.isExpanded && state!.cacheReasoning !== displayContent) {
						renderSafeHTML(contentEl, displayContent);
						state!.cacheReasoning = displayContent;
					}
				};

				stateMap.set(parentEl, state);
			}

			const isActivelyThinking = isGenerating && msg.role === "assistant" && !hasTextContent(msg);

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
