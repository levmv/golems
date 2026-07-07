import {
	ChatUI,
	RemoteStorage,
	type ChatProvider,
	type ChatPlugin,
	type ChatRequest,
	type FinishReason,
	type Message,
	type StreamEvent,
} from "murm-ui/with-css";
import { highlight } from "murm-ui/highlighter";
import "murm-ui/highlighter/theme.css";
import { AgentThinkingPlugin } from "murm-ui/plugins/agent-thinking";
import { CopyPlugin } from "murm-ui/plugins/copy";
import { ToolsPlugin } from "murm-ui/plugins/tools";
import "./app.css";

interface ChatListResponse {
	items?: Array<{ id: string }>;
}

const scheduledTurnClass = "caliban-scheduled-turn";
const scheduledTurnExpandedClass = "is-expanded";

interface ScheduledTurnBlockState {
	onClick: (event: MouseEvent) => void;
	onKeyDown: (event: KeyboardEvent) => void;
	expanded: boolean;
}

class CalibanProvider implements ChatProvider {
	constructor(private readonly chatID: string) {}

	async streamChat(request: ChatRequest, onEvent: (event: StreamEvent) => void): Promise<void> {
		const input = lastUserText(request);
		if (!input.trim()) throw new Error("No user message to send.");

		const clientRunId = lastUserRunID(request);
		const events = new EventSource(
			`/api/chats/${encodeURIComponent(this.chatID)}/events?scope=runs&client_run_id=${encodeURIComponent(clientRunId)}`,
		);
		let finished = false;
		let streamOpened = false;
		let rejectOpen: (error: Error) => void = () => {};
		let rejectPromise: (error: Error) => void = () => {};

		const opened = new Promise<void>((resolve, reject) => {
			rejectOpen = reject;
			events.onopen = () => {
				streamOpened = true;
				resolve();
			};
		});

		const done = new Promise<void>((resolve, reject) => {
			rejectPromise = reject;
			const forward = (event: MessageEvent<string>) => {
				try {
					const payload = JSON.parse(event.data) as StreamEvent;
					onEvent(payload);
					if (payload.type === "finish") {
						finished = true;
						resolve();
					}
				} catch (error) {
					reject(error instanceof Error ? error : new Error(String(error)));
				}
			};

			for (const type of [
				"message_start",
				"text_delta",
				"reasoning_delta",
				"tool_call_start",
				"tool_call_delta",
				"tool_result",
				"artifact",
				"usage",
				"finish",
			]) {
				events.addEventListener(type, forward as EventListener);
			}
		});

		events.onerror = () => {
			if (finished) return;
			const error = new Error("Event stream disconnected.");
			if (!streamOpened) rejectOpen(error);
			rejectPromise(error);
		};

		request.signal.addEventListener(
			"abort",
			() => {
				events.close();
				onEvent({ type: "finish", reason: "aborted" as FinishReason });
				rejectPromise(new DOMException("Aborted", "AbortError"));
			},
			{ once: true },
		);

		try {
			await opened;
			const response = await fetch(`/api/chats/${encodeURIComponent(this.chatID)}/runs`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({
					input,
					clientMessageId: clientRunId,
					stream: false,
				}),
				signal: request.signal,
			});
			if (!response.ok) throw new Error(await errorText(response));
			await done;
		} finally {
			events.close();
		}
	}
}

function lastUserText(request: ChatRequest): string {
	for (let i = request.messages.length - 1; i >= 0; i--) {
		const message = request.messages[i];
		if (message.role !== "user") continue;
		return message.blocks
			.filter((block) => block.type === "text")
			.map((block) => block.text)
			.join("\n");
	}
	return "";
}

function lastUserRunID(request: ChatRequest): string {
	for (let i = request.messages.length - 1; i >= 0; i--) {
		const message = request.messages[i];
		if (message.role === "user") return message.runId ?? message.id;
	}
	return "";
}

async function errorText(response: Response): Promise<string> {
	const body = await response.text();
	try {
		const parsed = JSON.parse(body) as { error?: string };
		return parsed.error || body || `HTTP ${response.status}`;
	} catch {
		return body || `HTTP ${response.status}`;
	}
}

void startApp().catch((error) => {
	console.error("Failed to start Caliban web app", error);
});

async function startApp(): Promise<void> {
	const chatID = await loadInitialChatID();
	const chatUI = new ChatUI({
		container: ".mur-app",
		provider: new CalibanProvider(chatID),
		storage: new RemoteStorage("/api", () => null),
		initialSessionId: chatID,
		routing: false,
		enableSidebar: false,
		agentRunCollapse: "machinery",
		plugins: () => [ScheduledTurnPlugin(), AgentThinkingPlugin(), ToolsPlugin(), CopyPlugin()],
		highlighter: highlight,
		updateWindowTitle: true,
	});

	const serviceWorkerReady = registerServiceWorker();
	armReminderSound();
	startPersistedMessageTail(chatUI, chatID);
	setupPushControls(serviceWorkerReady, chatID);
}

async function loadInitialChatID(): Promise<string> {
	const response = await fetch("/api/chats", { cache: "no-store" });
	if (!response.ok) throw new Error(await errorText(response));
	const payload = (await response.json()) as ChatListResponse;
	const id = payload.items?.[0]?.id?.trim();
	if (!id) throw new Error("No web chat is available.");
	return id;
}

function ScheduledTurnPlugin(): ChatPlugin {
	const blockStates = new WeakMap<HTMLElement, ScheduledTurnBlockState>();
	const setExpanded = (container: HTMLElement, state: ScheduledTurnBlockState, expanded: boolean) => {
		state.expanded = expanded;
		container.classList.toggle(scheduledTurnExpandedClass, expanded);
		container.setAttribute("aria-expanded", String(expanded));
		container.title = expanded ? "Collapse scheduled prompt" : "Expand scheduled prompt";
		container.setAttribute("aria-label", container.title);
	};

	return {
		name: "caliban-scheduled-turn",
		onBlockRender(block, container, _isGenerating, ctx) {
			if (block.type !== "text") return false;

			const isScheduledTurn = ctx?.message.role === "user" && ctx.message.meta?.source === "schedule";
			if (isScheduledTurn) {
					if (!blockStates.has(container)) {
						const state: ScheduledTurnBlockState = {
							expanded: false,
							onClick: (event) => {
								const target = event.target;
								if (target instanceof HTMLElement && target.closest("a,button,input,select,textarea")) return;
								setExpanded(container, state, !state.expanded);
							},
							onKeyDown: (event) => {
								if (event.key !== "Enter" && event.key !== " ") return;
								event.preventDefault();
								setExpanded(container, state, !state.expanded);
							},
						};
					container.classList.add(scheduledTurnClass);
					container.setAttribute("role", "button");
					container.tabIndex = 0;
						container.addEventListener("click", state.onClick);
						container.addEventListener("keydown", state.onKeyDown);
						blockStates.set(container, state);
						setExpanded(container, state, false);
					}
				} else {
					const state = blockStates.get(container);
					if (!state) return false;
				container.classList.remove(scheduledTurnClass);
					container.classList.remove(scheduledTurnExpandedClass);
					container.removeAttribute("role");
					container.removeAttribute("tabindex");
					container.removeAttribute("aria-expanded");
					container.removeAttribute("aria-label");
					container.removeAttribute("title");
					container.removeEventListener("click", state.onClick);
					container.removeEventListener("keydown", state.onKeyDown);
					blockStates.delete(container);
			}
			return false;
		},
	};
}

function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
	if (!("serviceWorker" in navigator)) return Promise.resolve(null);
	return navigator.serviceWorker
		.register("/sw.js")
		.then(() => navigator.serviceWorker.ready)
		.catch((error) => {
			console.error("Failed to register service worker", error);
			return null;
		});
}

function startPersistedMessageTail(ui: ChatUI, chatID: string): void {
	let events: EventSource | null = null;
	const pending: Message[] = [];
	let flushChain = Promise.resolve();

	const open = () => {
		if (events || document.visibilityState !== "visible") return;
		events = new EventSource(`/api/chats/${encodeURIComponent(chatID)}/events?scope=messages`);
		events.addEventListener("message_start", onMessageStart as EventListener);
		events.onerror = () => {
			if (document.visibilityState !== "visible") close();
		};
	};

	const close = () => {
		if (!events) return;
		events.close();
		events = null;
	};

	const enqueueFlush = () => {
		flushChain = flushChain
			.then(async () => {
				const state = ui.engine.state;
				if (state.isLoadingSession || state.generatingMessageId !== null) return;
				if (state.currentSessionId !== chatID) {
					pending.length = 0;
					return;
				}
				if (pending.length === 0) return;

				const messages = [...state.messages];
				const seen = new Set(messages.map((message) => message.id));
				const appended: Message[] = [];
				let changed = false;
				for (const message of pending.splice(0)) {
					if (seen.has(message.id)) continue;
					seen.add(message.id);
					messages.push(message);
					appended.push(message);
					changed = true;
				}
				if (changed) {
					const saved = await ui.engine.setMessages(messages);
					if (!saved) {
						pending.unshift(...appended);
						return;
					}
					for (const message of appended) handleReminderEffects(message);
				}
			})
			.catch((error) => console.error("Failed to append live message", error));
	};

	const onMessageStart = (event: MessageEvent<string>) => {
		try {
			const payload = JSON.parse(event.data) as StreamEvent;
			if (payload.type !== "message_start") return;
			pending.push(payload.message as Message);
			enqueueFlush();
		} catch (error) {
			console.error("Failed to parse live message", error);
		}
	};

	const syncOnVisible = async () => {
		if (document.visibilityState !== "visible") return;
		const state = ui.engine.state;
		if (state.currentSessionId !== chatID || state.isLoadingSession || state.generatingMessageId !== null) return;
		try {
			const response = await fetch(`/api/chats/${encodeURIComponent(chatID)}`, { cache: "no-store" });
			if (!response.ok) return;
			const chat = (await response.json()) as { messages?: Message[] };
			if (!Array.isArray(chat.messages)) return;
			const current = ui.engine.state;
			if (current.currentSessionId !== chatID || current.isLoadingSession || current.generatingMessageId !== null) return;

			// The backend returns only the latest tail. Replacing the whole array
			// would drop any older pages the user loaded via scroll-to-top, so keep
			// the already-loaded history that precedes the tail and refresh the rest
			// from the authoritative fetch (which catches up messages missed while
			// the tab was hidden).
			const tail = chat.messages;
			const tailIds = new Set(tail.map((m) => m.id));
			const overlapStart = current.messages.findIndex((m) => tailIds.has(m.id));
			const older = overlapStart === -1 ? current.messages : current.messages.slice(0, overlapStart);
			const merged = [...older, ...tail];

			// Skip the churn when nothing was missed (same length, same newest id).
			const last = merged[merged.length - 1]?.id;
			const currentLast = current.messages[current.messages.length - 1]?.id;
			if (merged.length === current.messages.length && last === currentLast) return;

			await ui.engine.setMessages(merged);
		} catch (error) {
			console.error("Failed to sync live messages", error);
		}
	};

	ui.engine.onChange(
		(state) => `${state.isLoadingSession}:${state.generatingMessageId ?? ""}`,
		() => enqueueFlush(),
	);
	document.addEventListener("visibilitychange", () => {
		if (document.visibilityState === "visible") {
			void syncOnVisible().finally(open);
			return;
		}
		close();
	});
	window.addEventListener("beforeunload", close, { once: true });
	open();
}

function handleReminderEffects(message: Message): void {
	if (message.meta?.source !== "reminder") return;
	playReminderSound();
	maybeShowBrowserNotification(message);
}

function maybeShowBrowserNotification(message: Message): void {
	if (message.meta?.source !== "reminder") return;
	if (document.visibilityState === "visible") return;
	if (!("Notification" in window) || Notification.permission !== "granted") return;

	const body = message.blocks
		.filter((block) => block.type === "text")
		.map((block) => block.text)
		.join("\n")
		.trim();
	if (!body) return;

	if ("serviceWorker" in navigator) {
		void navigator.serviceWorker.ready
			.then((registration) => registration.showNotification("⏰", { body, tag: message.id }))
			.catch(() => new Notification("⏰", { body, tag: message.id }));
		return;
	}
	new Notification("⏰", { body, tag: message.id });
}

let reminderAudio: AudioContext | null = null;

function armReminderSound(): void {
	const arm = () => {
		const ctor = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
		if (!ctor) return;
		reminderAudio ??= new ctor();
		void reminderAudio.resume().catch(() => undefined);
	};
	window.addEventListener("pointerdown", arm, { once: true, passive: true });
	window.addEventListener("keydown", arm, { once: true });
}

function playReminderSound(): void {
	if (document.visibilityState !== "visible") return;
	const ctx = reminderAudio;
	if (!ctx || ctx.state !== "running") return;

	const start = ctx.currentTime + 0.01;
	for (const [offset, frequency] of [
		[0, 880],
		[0.13, 1175],
	] as const) {
		const osc = ctx.createOscillator();
		const gain = ctx.createGain();
		osc.type = "sine";
		osc.frequency.value = frequency;
		gain.gain.setValueAtTime(0.0001, start + offset);
		gain.gain.exponentialRampToValueAtTime(0.055, start + offset + 0.015);
		gain.gain.exponentialRampToValueAtTime(0.0001, start + offset + 0.11);
		osc.connect(gain);
		gain.connect(ctx.destination);
		osc.start(start + offset);
		osc.stop(start + offset + 0.12);
	}
}

async function setupPushControls(serviceWorkerReady: Promise<ServiceWorkerRegistration | null>, chatID: string): Promise<void> {
	let config: { enabled: boolean; publicKey?: string };
	try {
		const response = await fetch("/api/push/config", { cache: "no-store" });
		if (!response.ok) return;
		config = (await response.json()) as { enabled: boolean; publicKey?: string };
	} catch (error) {
		console.error("Failed to load push config", error);
		return;
	}
	const publicKey = config.publicKey;
	if (!config.enabled || !publicKey) return;
	if (!("Notification" in window) || !("PushManager" in window)) return;

	const registration = await serviceWorkerReady;
	if (!registration?.pushManager) return;

	const button = document.createElement("button");
	button.className = "caliban-push-button";
	button.type = "button";
	button.title = "Enable push notifications";
	button.setAttribute("aria-label", "Enable push notifications");
	button.innerHTML = bellIcon();
	document.body.appendChild(button);

	const syncState = async () => {
		const subscription = await registration.pushManager.getSubscription();
		const enabled = Notification.permission === "granted" && subscription !== null;
		button.classList.toggle("is-enabled", enabled);
		button.classList.toggle("is-blocked", Notification.permission === "denied");
		button.title = enabled ? "Push notifications enabled" : "Enable push notifications";
		button.setAttribute("aria-label", button.title);
		if (enabled) await savePushSubscription(subscription, chatID);
	};

	button.addEventListener("click", async () => {
		try {
			if (Notification.permission === "default") {
				const permission = await Notification.requestPermission();
				if (permission !== "granted") {
					await syncState();
					return;
				}
			}
			if (Notification.permission !== "granted") {
				await syncState();
				return;
			}
			const existing = await registration.pushManager.getSubscription();
			const subscription =
				existing ??
				(await registration.pushManager.subscribe({
					userVisibleOnly: true,
					applicationServerKey: urlBase64ToArrayBuffer(publicKey),
				}));
			await savePushSubscription(subscription, chatID);
			await syncState();
		} catch (error) {
			console.error("Failed to enable push notifications", error);
		}
	});

	await syncState();
}

async function savePushSubscription(subscription: PushSubscription, chatID: string): Promise<void> {
	const response = await fetch(`/api/chats/${encodeURIComponent(chatID)}/push-subscriptions`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(subscription.toJSON()),
	});
	if (!response.ok) throw new Error(await errorText(response));
}

function urlBase64ToArrayBuffer(value: string): ArrayBuffer {
	const padding = "=".repeat((4 - (value.length % 4)) % 4);
	const base64 = (value + padding).replace(/-/g, "+").replace(/_/g, "/");
	const raw = atob(base64);
	const buffer = new ArrayBuffer(raw.length);
	const out = new Uint8Array(buffer);
	for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
	return buffer;
}

function bellIcon(): string {
	return `<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.27 21a2 2 0 0 0 3.46 0"/><path d="M3.26 15.33A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.67C19.4 13.86 18 12.08 18 8a6 6 0 0 0-12 0c0 4.08-1.4 5.86-2.74 7.33"/></svg>`;
}
