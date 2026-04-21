import "./attachment.css";
import type { Attachment, ChatPlugin, Message, PluginInputContext } from "../../core/types";
import { el } from "../../utils/dom";
import { ICON_PAPERCLIP } from "../../utils/icons";

export interface AttachmentPluginConfig {
	/** Maximum file size in bytes. Default: 20MB */
	maxFileSize?: number;
	/** Callback when a file exceeds the limit. Defaults to window.alert */
	onSizeExceeded?: (file: File, maxSize: number) => void;
}

export function AttachmentPlugin(config?: AttachmentPluginConfig): ChatPlugin {
	const maxSize = config?.maxFileSize ?? 20 * 1024 * 1024;
	const handleSizeExceeded =
		config?.onSizeExceeded ??
		((file, max) => {
			alert(`File "${file.name}" exceeds the maximum allowed size of ${Math.round(max / (1024 * 1024))}MB.`);
		});

	const stateMap = new WeakMap<HTMLElement, HTMLElement>();

	// The plugin owns its own state now!
	let pendingAttachments: Attachment[] = [];

	let fileInput: HTMLInputElement;
	let previewContainer: HTMLElement;
	let attachBtn: HTMLButtonElement;
	let boundOnChange: () => void;

	const renderPreviews = () => {
		if (!previewContainer) return;
		previewContainer.innerHTML = "";
		previewContainer.style.display = pendingAttachments.length ? "flex" : "none";

		pendingAttachments.forEach((att) => {
			const item = el("div", "attachment-preview-item");

			if (att.type === "image") {
				item.appendChild(el("img", "", { src: att.data, alt: att.name }));
			} else {
				item.appendChild(el("div", "file-preview", { textContent: `📄 ${att.name}` }));
			}

			const removeBtn = el("button", "attachment-remove-btn", {
				innerHTML: "×",
				type: "button",
				onclick: () => {
					pendingAttachments = pendingAttachments.filter((a) => a !== att);
					renderPreviews();
				},
			});

			item.appendChild(removeBtn);
			previewContainer.appendChild(item);
		});
	};

	return {
		name: "attachments",

		onInputMount: (ctx: PluginInputContext) => {
			previewContainer = el("div", "attachment-previews");
			previewContainer.style.display = "none";

			fileInput = el("input", "", { type: "file", hidden: true, multiple: true });

			attachBtn = el("button", "form-icon-btn", {
				type: "button",
				innerHTML: ICON_PAPERCLIP,
				onclick: () => fileInput.click(),
			});

			ctx.form.prepend(attachBtn);
			ctx.form.parentElement!.prepend(previewContainer);
			ctx.form.appendChild(fileInput);

			boundOnChange = async () => {
				const files = Array.from(fileInput.files || []);
				for (const file of files) {
					if (file.size > maxSize) {
						handleSizeExceeded(file, maxSize);
						continue;
					}

					const isImage = file.type.startsWith("image/");
					const reader = new FileReader();

					reader.onload = () => {
						pendingAttachments.push({
							type: isImage ? "image" : "file",
							name: file.name,
							data: reader.result as string,
						});
						renderPreviews();
					};

					if (isImage) reader.readAsDataURL(file);
					else reader.readAsText(file);
				}
				fileInput.value = "";
			};
			fileInput.addEventListener("change", boundOnChange);
		},

		hasPendingData: () => pendingAttachments.length > 0,

		onUserSubmit: (msg) => {
			if (pendingAttachments.length > 0) {
				msg.attachments = [...pendingAttachments];
				pendingAttachments = [];
				renderPreviews();
			}
		},

		onMessageRender: (msg: Message, parentEl: HTMLElement) => {
			if (!msg.attachments || msg.attachments.length === 0) return;

			let container = stateMap.get(parentEl);
			if (!container) {
				container = el("div", "message-attachments");
				parentEl.prepend(container);

				for (const att of msg.attachments) {
					if (att.type === "image") {
						container.appendChild(el("img", "attachment-image", { src: att.data }));
					} else {
						container.appendChild(el("div", "attachment-file-pill", { textContent: `📄 ${att.name}` }));
					}
				}
				stateMap.set(parentEl, container);
			}
		},

		destroy: () => {
			if (fileInput && boundOnChange) {
				fileInput.removeEventListener("change", boundOnChange);
			}
			fileInput?.remove();
			attachBtn?.remove();
			previewContainer?.remove();
			pendingAttachments = [];
		},
	};
}
