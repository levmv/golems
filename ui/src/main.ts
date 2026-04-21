import { LLMChat } from "./core/chat";
import type { ChatPlugin, ProviderAdapter, StorageAdapter } from "./core/types";
import { AppRouter, type RouterConfig } from "./router";
import { queryOrThrow } from "./utils/dom";

import "./styles/base.css";
import "./styles/sidebar.css";
import "./styles/input.css";
import "./styles/feed.css";

import { Feed } from "./components/feed";
import { Input } from "./components/input";
import { Sidebar } from "./components/sidebar";

export interface ChatUIConfig {
	container: HTMLElement | string;
	provider: ProviderAdapter;
	storage: StorageAdapter;
	routing?: RouterConfig | boolean;

	showSidebar?: boolean;

	highlighter?: (code: string, language: string) => string;
	plugins?: (chatApi: LLMChat) => ChatPlugin[];
}

export class ChatUI {
	private chat: LLMChat;
	private container: HTMLElement;
	private config: ChatUIConfig;
	private router: AppRouter;

	private inputComponent!: Input;
	private feedComponent!: Feed;
	private sidebarComponent!: Sidebar;
	private plugins: ChatPlugin[] = [];

	private elements!: {
		mainArea: HTMLElement;
		sidebarEl: HTMLElement;
		openSidebarBtn: HTMLButtonElement;
		headerTitle: HTMLElement;
	};

	private onMainAreaClickBound = () => this.closeSidebar(true);
	private onOpenSidebarBound = (e: MouseEvent) => {
		e.stopPropagation();
		this.openSidebar();
	};

	constructor(config: ChatUIConfig) {
		this.config = { showSidebar: true, ...config };

		let routerConfig: RouterConfig = { type: "hash" };
		if (this.config.routing === false) {
			routerConfig = { type: "none" };
		} else if (typeof this.config.routing === "object") {
			routerConfig = this.config.routing;
		}

		this.router = new AppRouter(routerConfig);

		const el =
			typeof this.config.container === "string" ? document.querySelector(this.config.container) : this.config.container;

		if (!el) throw new Error(`Chat container not found: ${this.config.container}`);
		this.container = el as HTMLElement;

		const initialSessionId = this.router.getId() || null;

		this.chat = new LLMChat({
			provider: this.config.provider,
			storage: this.config.storage,
			initialSessionId,
		});

		this.initComponents();
		this.applyConfig();
		this.bindEvents();
	}

	public async destroy() {
		this.router.destroy();
		await this.chat.destroy();

		if (this.config.showSidebar) {
			this.elements.openSidebarBtn.removeEventListener("click", this.onOpenSidebarBound);
			this.elements.mainArea.removeEventListener("click", this.onMainAreaClickBound);
		}

		for (const plugin of this.plugins) {
			if (plugin.destroy) plugin.destroy();
		}

		this.sidebarComponent.destroy();
		this.feedComponent.destroy();
		this.inputComponent.destroy();
	}

	private initComponents() {
		this.plugins = this.config.plugins ? this.config.plugins(this.chat) : [];
		this.chat.registerPlugins(this.plugins);

		const mainHeader = queryOrThrow<HTMLElement>(this.container, ".llm-main-header");

		this.elements = {
			mainArea: queryOrThrow<HTMLElement>(this.container, ".llm-main-area"),
			sidebarEl: queryOrThrow<HTMLElement>(this.container, ".llm-sidebar"),
			openSidebarBtn: queryOrThrow<HTMLButtonElement>(mainHeader, ".llm-open-sidebar-btn"),
			headerTitle: queryOrThrow<HTMLElement>(mainHeader, ".llm-header-title"),
		};

		const pluginCtx = {
			chat: this.chat,
			elements: {
				container: this.container,
				sidebar: this.elements.sidebarEl,
				header: mainHeader,
			},
		};

		for (const plugin of this.plugins) {
			if (plugin.onMount) plugin.onMount(pluginCtx);
		}

		this.inputComponent = new Input(
			{
				container: this.container,
				onSubmit: async (text) => await this.chat.sendMessage(text),
				onStop: () => this.chat.stopGeneration(),
			},
			this.plugins,
		);

		this.feedComponent = new Feed(this.container, {
			highlighter: this.config.highlighter,
			plugins: this.plugins,
		});

		this.sidebarComponent = new Sidebar({
			container: this.container,
			onNewChat: () => {
				this.chat.createNewSession();
				this.closeSidebar(true);
				this.inputComponent.focus();
			},
			onSelectSession: (id) => {
				this.chat.switchSession(id);
				this.closeSidebar(true);
			},
			onDeleteSession: (id) => {
				this.chat.deleteSession(id);
			},
			onLoadMore: () => {
				this.chat.loadMoreSessions();
			},
			onClose: () => {
				this.closeSidebar(false);
			},
			getSessionHref: (id) => this.router.hrefFor(id),
		});
	}

	private applyConfig() {
		if (this.config.showSidebar === false) {
			this.sidebarComponent.setVisible(false);
			this.elements.openSidebarBtn.style.display = "none";
			this.container.classList.add("sidebar-closed");
			return;
		}
		const isDesktopClosed = lsGetItem("llm_sidebar_closed") === "true";

		if (isDesktopClosed && window.innerWidth > 768) {
			this.elements.sidebarEl.classList.add("hidden-desktop");
			this.container.classList.add("sidebar-closed");
		}
	}

	private bindEvents() {
		if (this.config.showSidebar) {
			this.elements.openSidebarBtn.addEventListener("click", this.onOpenSidebarBound);
			this.elements.mainArea.addEventListener("click", this.onMainAreaClickBound);
		}

		this.router.listen((id) => {
			if (id) {
				this.chat.switchSession(id);
			} else {
				this.chat.createNewSession();
			}
		});

		this.chat.store.subscribe(
			(state) => state.sessions,
			(sessions) => {
				const state = this.chat.store.get();
				if (this.config.showSidebar) {
					this.sidebarComponent.renderSessions(sessions, state.currentSessionId, state.hasMoreSessions);
				}
				this.updateHeaderTitle();
			},
		);

		this.chat.store.subscribe(
			(state) => state.currentSessionId,
			(currentSessionId) => {
				if (this.config.showSidebar) {
					this.sidebarComponent.setActiveSession(currentSessionId);
				}
				this.updateHeaderTitle();
			},
		);

		let prevIsGenerating = false;

		// We subscribe globally without a selector because stream chunks are applied
		// via in-place mutation (for performance), which bypasses selector equality checks.
		this.chat.store.subscribeGlobal((state) => {
			const shouldHaveUrlId = state.messages.length > 0 || state.isLoadingSession;
			const targetId = shouldHaveUrlId ? state.currentSessionId : null;

			if (this.router.getId() !== targetId) {
				// If we fell back to an empty chat due to a loading error (e.g., broken link),
				// use replace so we don't trap the user's Back button.
				const isErrorFallback = !shouldHaveUrlId && state.error !== null;
				this.router.setUrl(targetId, isErrorFallback);
			}

			const generationStarted = !prevIsGenerating && state.isGenerating;
			this.feedComponent.update(
				state.messages,
				state.isGenerating,
				state.isLoadingSession,
				generationStarted,
				state.error,
			);
			prevIsGenerating = state.isGenerating;
		});

		this.chat.store.subscribe(
			(state) => state.currentSessionId,
			() => {
				this.inputComponent.setText("");
			},
		);

		this.chat.store.subscribe(
			(state) => (state.isGenerating ? 2 : 0) | (state.isLoadingSession ? 1 : 0),
			(bits) => {
				const isGenerating = !!(bits & 2);
				const isLoadingSession = !!(bits & 1);

				this.inputComponent.setGeneratingState(isGenerating, isLoadingSession);
			},
		);
	}

	private updateHeaderTitle() {
		const state = this.chat.store.get();
		const activeSession = state.sessions.find((s) => s.id === state.currentSessionId);
		this.elements.headerTitle.textContent = activeSession ? activeSession.title : "New Chat";
	}

	private openSidebar() {
		const isMobile = window.innerWidth <= 768;

		if (isMobile) {
			this.elements.sidebarEl.classList.add("mobile-open");
		} else {
			this.elements.sidebarEl.classList.remove("hidden-desktop");
			this.container.classList.remove("sidebar-closed");
			lsSetItem("llm_sidebar_closed", "false");
		}
	}

	private closeSidebar(isNavigation = false) {
		const isMobile = window.innerWidth <= 768;

		if (isMobile) {
			this.elements.sidebarEl.classList.remove("mobile-open");
		} else {
			if (isNavigation) return;

			this.elements.sidebarEl.classList.add("hidden-desktop");
			this.container.classList.add("sidebar-closed");
			lsSetItem("llm_sidebar_closed", "true");
		}
	}
}

function lsGetItem(key: string): string | null {
	try {
		return localStorage.getItem(key);
	} catch {
		return null;
	}
}

function lsSetItem(key: string, value: string): void {
	try {
		localStorage.setItem(key, value);
	} catch {
		// Ignore
	}
}
